package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/alert"
	"github.com/PCenjoyer/TradingMaster/internal/backtest"
	"github.com/PCenjoyer/TradingMaster/internal/database"
	"github.com/PCenjoyer/TradingMaster/internal/httpapi"
	"github.com/PCenjoyer/TradingMaster/internal/marketdata"
	"github.com/PCenjoyer/TradingMaster/internal/paper"
	"github.com/PCenjoyer/TradingMaster/internal/risk"
	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/PCenjoyer/TradingMaster/internal/shadow"
	"github.com/PCenjoyer/TradingMaster/internal/strategy"
	"github.com/PCenjoyer/TradingMaster/internal/testexchange"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("приложение завершилось с ошибкой", "ошибка", err)
		os.Exit(1)
	}
}

func run() error {
	mode := flag.String("mode", envOrDefault("TM_MODE", "api"), "режим: api, backtest или healthcheck")
	address := flag.String("addr", envOrDefault("TM_HTTP_ADDR", ":8080"), "адрес HTTP-сервера")
	dataPath := flag.String("data", envOrDefault("TM_DATA_FILE", ""), "путь к CSV для бэктеста")
	flag.Parse()

	switch *mode {
	case "api":
		return serve(*address)
	case "backtest":
		return runBacktest(*dataPath)
	case "healthcheck":
		return healthcheck(envOrDefault("TM_HEALTH_URL", "http://127.0.0.1:8080/healthz"))
	default:
		return fmt.Errorf("неизвестный режим %q", *mode)
	}
}

func serve(address string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dailyLossLimit, err := envFloat("TM_DAILY_LOSS_LIMIT", 0.03)
	if err != nil {
		return err
	}
	notifier, err := notifierFromEnv()
	if err != nil {
		return err
	}
	adminToken := os.Getenv("TM_ADMIN_TOKEN")
	initialKillSwitch, err := envBool("TM_KILL_SWITCH_DEFAULT", false)
	if err != nil {
		return err
	}
	if adminToken == "" {
		logger.Warn("административный API безопасности отключён: TM_ADMIN_TOKEN не настроен")
	}
	if !notifier.Enabled() {
		logger.Warn("Telegram-уведомления отключены")
	}
	safetyConfig := safety.Config{
		DailyLossLimit: dailyLossLimit, InitialActive: initialKillSwitch,
		InitialReason: "безопасная блокировка при запуске сервиса",
	}
	controller, err := safety.NewController(safetyConfig)
	if err != nil {
		return err
	}
	var paperBroker paper.Broker
	var testnetBroker testexchange.Broker
	var shadowService shadow.Service
	testnetEnabled, err := envBool("TM_BINANCE_TESTNET_ENABLED", false)
	if err != nil {
		return err
	}
	shadowEnabled, err := envBool("TM_SHADOW_ENABLED", false)
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("TM_DATABASE_URL")
	if databaseURL == "" {
		if testnetEnabled {
			return fmt.Errorf("Binance Spot Testnet требует TM_DATABASE_URL для durable-журнала")
		}
		if shadowEnabled {
			return fmt.Errorf("shadow trading требует TM_DATABASE_URL для durable-журнала и выбора лидера")
		}
		logger.Warn("PostgreSQL не настроен: safety-state хранится в памяти, paper trading отключён")
	} else {
		startupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool, openErr := database.Open(startupContext, databaseURL)
		if openErr != nil {
			return openErr
		}
		defer pool.Close()
		if err := database.Migrate(startupContext, pool); err != nil {
			return err
		}
		controller, err = safety.NewPostgresController(startupContext, pool, safetyConfig)
		if err != nil {
			return err
		}
		paperConfig, configErr := paperConfigFromEnv()
		if configErr != nil {
			return configErr
		}
		paperBroker, err = paper.NewPostgresBroker(startupContext, pool, controller, paperConfig)
		if err != nil {
			return err
		}
		if testnetEnabled {
			testnetConfig, configErr := testnetConfigFromEnv()
			if configErr != nil {
				return configErr
			}
			testnetBroker, err = testexchange.NewBinanceSpotBroker(startupContext, pool, controller, testnetConfig)
			if err != nil {
				return err
			}
			logger.Info("подключён Binance Spot Testnet", "режим_заявок", testnetBroker.Mode())
		}
		if shadowEnabled {
			shadowConfig, configErr := shadowConfigFromEnv()
			if configErr != nil {
				return configErr
			}
			shadowService, err = shadow.NewPostgresService(pool, controller, logger, shadowConfig)
			if err != nil {
				return err
			}
			logger.Info("подключён shadow trading", "символы", shadowConfig.Symbols, "интервал", shadowConfig.Interval)
		}
		logger.Info("подключено общее PostgreSQL-хранилище", "paper_trading", true)
	}
	api, err := httpapi.NewServer(logger, version, httpapi.Config{
		AdminToken: adminToken, DailyLossLimit: dailyLossLimit,
		InitialKillSwitch: initialKillSwitch, Notifier: notifier,
		Safety: controller, Paper: paperBroker, TestExchange: testnetBroker, Shadow: shadowService,
	})
	if err != nil {
		return fmt.Errorf("настроить API: %w", err)
	}
	server := &http.Server{
		Addr: address, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 20 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
	workerErrors := make(chan error, 1)
	if shadowService != nil && shadowService.Enabled() {
		go func() {
			if err := shadowService.Run(ctx); err != nil {
				workerErrors <- err
			}
		}()
	}
	go func() {
		logger.Info("HTTP-сервер запущен", "адрес", address, "версия", version)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP-сервер: %w", err)
		}
		return nil
	case err := <-workerErrors:
		return fmt.Errorf("shadow trading: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func runBacktest(dataPath string) error {
	if dataPath == "" {
		return errors.New("для режима backtest укажите -data /путь/к/свечам.csv")
	}
	candles, err := marketdata.ReadCSV(dataPath)
	if err != nil {
		return err
	}
	executionConfig := backtest.DefaultConfig()
	strategyEngine, err := strategy.NewTrendBreakout(strategy.DefaultConfig())
	if err != nil {
		return err
	}
	riskManager, err := risk.NewManager(risk.DefaultConfig(), executionConfig.InitialCapital)
	if err != nil {
		return err
	}
	engine, err := backtest.NewEngine(executionConfig, strategyEngine, riskManager)
	if err != nil {
		return err
	}
	result, err := engine.Run(candles)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func healthcheck(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("проверка здоровья: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("проверка здоровья вернула HTTP %d", response.StatusCode)
	}
	return nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envFloat(name string, fallback float64) (float64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s должно быть числом: %w", name, err)
	}
	return value, nil
}

func envBool(name string, fallback bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s должно быть true или false: %w", name, err)
	}
	return value, nil
}

func envInt(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s должно быть целым числом: %w", name, err)
	}
	return value, nil
}

func notifierFromEnv() (alert.Notifier, error) {
	token := os.Getenv("TM_TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TM_TELEGRAM_CHAT_ID")
	if token == "" && chatID == "" {
		return alert.Noop{}, nil
	}
	if token == "" || chatID == "" {
		return nil, fmt.Errorf("TM_TELEGRAM_BOT_TOKEN и TM_TELEGRAM_CHAT_ID должны быть заданы вместе")
	}
	return alert.NewTelegram(alert.TelegramConfig{BotToken: token, ChatID: chatID})
}

func paperConfigFromEnv() (paper.Config, error) {
	initialCapital, err := envFloat("TM_PAPER_INITIAL_CAPITAL", 10_000)
	if err != nil {
		return paper.Config{}, err
	}
	feeBPS, err := envFloat("TM_PAPER_FEE_BPS", 10)
	if err != nil {
		return paper.Config{}, err
	}
	slippageBPS, err := envFloat("TM_PAPER_SLIPPAGE_BPS", 5)
	if err != nil {
		return paper.Config{}, err
	}
	return paper.Config{InitialCapital: initialCapital, FeeBPS: feeBPS, SlippageBPS: slippageBPS}, nil
}

func testnetConfigFromEnv() (testexchange.Config, error) {
	receiveWindowMilliseconds, err := envFloat("TM_BINANCE_TESTNET_RECV_WINDOW_MS", 5_000)
	if err != nil {
		return testexchange.Config{}, err
	}
	if receiveWindowMilliseconds != float64(int64(receiveWindowMilliseconds)) {
		return testexchange.Config{}, fmt.Errorf("TM_BINANCE_TESTNET_RECV_WINDOW_MS должно быть целым числом")
	}
	return testexchange.Config{
		APIKey:        os.Getenv("TM_BINANCE_TESTNET_API_KEY"),
		SecretKey:     os.Getenv("TM_BINANCE_TESTNET_SECRET_KEY"),
		Mode:          testexchange.Mode(envOrDefault("TM_BINANCE_TESTNET_ORDER_MODE", string(testexchange.ModeValidate))),
		ReceiveWindow: time.Duration(int64(receiveWindowMilliseconds)) * time.Millisecond,
	}, nil
}

func shadowConfigFromEnv() (shadow.Config, error) {
	historyLimit, err := envInt("TM_SHADOW_HISTORY_LIMIT", 120)
	if err != nil {
		return shadow.Config{}, err
	}
	rawSymbols := envOrDefault("TM_SHADOW_SYMBOLS", "BTCUSDT")
	symbols := strings.Split(rawSymbols, ",")
	return shadow.Config{
		Symbols: symbols, Interval: envOrDefault("TM_SHADOW_INTERVAL", "1m"),
		HistoryLimit: historyLimit, Strategy: strategy.DefaultConfig(),
	}, nil
}
