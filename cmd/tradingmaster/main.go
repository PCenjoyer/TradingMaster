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
	"syscall"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/backtest"
	"github.com/PCenjoyer/TradingMaster/internal/httpapi"
	"github.com/PCenjoyer/TradingMaster/internal/marketdata"
	"github.com/PCenjoyer/TradingMaster/internal/risk"
	"github.com/PCenjoyer/TradingMaster/internal/strategy"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("приложение завершилось с ошибкой", "ошибка", err)
		os.Exit(1)
	}
}

func run() error {
	mode := flag.String("mode", envOrDefault("TM_MODE", "api"), "режим: api или backtest")
	address := flag.String("addr", envOrDefault("TM_HTTP_ADDR", ":8080"), "адрес HTTP-сервера")
	dataPath := flag.String("data", envOrDefault("TM_DATA_FILE", ""), "путь к CSV для бэктеста")
	flag.Parse()

	switch *mode {
	case "api":
		return serve(*address)
	case "backtest":
		return runBacktest(*dataPath)
	default:
		return fmt.Errorf("неизвестный режим %q", *mode)
	}
}

func serve(address string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	api := httpapi.NewServer(logger, version)
	server := &http.Server{
		Addr: address, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 20 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
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

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
