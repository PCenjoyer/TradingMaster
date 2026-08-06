package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/alert"
	"github.com/PCenjoyer/TradingMaster/internal/backtest"
	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/PCenjoyer/TradingMaster/internal/observability"
	"github.com/PCenjoyer/TradingMaster/internal/paper"
	"github.com/PCenjoyer/TradingMaster/internal/risk"
	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/PCenjoyer/TradingMaster/internal/strategy"
	"github.com/PCenjoyer/TradingMaster/internal/testexchange"
)

const maxRequestSize = 16 << 20

type BacktestRequest struct {
	Candles   []domain.Candle `json:"candles"`
	Strategy  strategy.Config `json:"strategy"`
	Risk      risk.Config     `json:"risk"`
	Execution backtest.Config `json:"execution"`
}

type Config struct {
	AdminToken        string
	DailyLossLimit    float64
	InitialKillSwitch bool
	Notifier          alert.Notifier
	Safety            *safety.Controller
	Paper             paper.Broker
	TestExchange      testexchange.Broker
}

func DefaultConfig() Config {
	return Config{DailyLossLimit: 0.03, Notifier: alert.Noop{}}
}

type Server struct {
	logger       *slog.Logger
	metrics      *observability.Metrics
	safety       *safety.Controller
	notifier     alert.Notifier
	paper        paper.Broker
	testExchange testexchange.Broker
	adminToken   string
	version      string
}

func NewServer(logger *slog.Logger, version string, configs ...Config) (*Server, error) {
	config := DefaultConfig()
	if len(configs) > 0 {
		config = configs[0]
		if config.DailyLossLimit == 0 {
			config.DailyLossLimit = DefaultConfig().DailyLossLimit
		}
		if config.Notifier == nil {
			config.Notifier = alert.Noop{}
		}
	}
	controller := config.Safety
	if controller == nil {
		var err error
		controller, err = safety.NewController(safety.Config{
			DailyLossLimit: config.DailyLossLimit,
			InitialActive:  config.InitialKillSwitch,
			InitialReason:  "безопасная блокировка при запуске сервиса",
		})
		if err != nil {
			return nil, err
		}
	}
	metrics := &observability.Metrics{}
	state, err := controller.State(context.Background())
	if err != nil {
		return nil, err
	}
	metrics.SetSafety(state.Active, state.DailyLossRatio, state.DailyLossLimit)
	metrics.SetDurableStore(controller.Durable())
	if config.Paper != nil && config.Paper.Enabled() {
		portfolio, portfolioErr := config.Paper.Portfolio(context.Background())
		if portfolioErr != nil {
			return nil, fmt.Errorf("прочитать начальный paper-портфель: %w", portfolioErr)
		}
		metrics.SetPaperEquity(portfolio.Equity)
	}
	server := &Server{
		logger: logger, metrics: metrics, safety: controller, notifier: config.Notifier,
		paper: config.Paper, testExchange: config.TestExchange,
		adminToken: config.AdminToken, version: version,
	}
	if state.Active && !controller.Durable() {
		server.notifyTransition(safety.Transition{
			Changed: true, Active: true, Engaged: true,
			Cause: state.Reason, Time: state.UpdatedAt,
		})
	}
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("POST /api/v1/backtests", s.backtest)
	mux.HandleFunc("GET /api/v1/safety", s.requireAdmin(s.safetyStatus))
	mux.HandleFunc("POST /api/v1/safety/kill-switch", s.requireAdmin(s.killSwitch))
	mux.HandleFunc("POST /api/v1/safety/equity", s.requireAdmin(s.observeEquity))
	mux.HandleFunc("POST /api/v1/paper/orders", s.requireAdmin(s.paperOrder))
	mux.HandleFunc("GET /api/v1/paper/orders", s.requireAdmin(s.paperOrders))
	mux.HandleFunc("GET /api/v1/paper/portfolio", s.requireAdmin(s.paperPortfolio))
	mux.HandleFunc("POST /api/v1/paper/marks", s.requireAdmin(s.paperMark))
	mux.HandleFunc("GET /api/v1/testnet/status", s.requireAdmin(s.testnetStatus))
	mux.HandleFunc("GET /api/v1/testnet/account", s.requireAdmin(s.testnetAccount))
	mux.HandleFunc("POST /api/v1/testnet/orders", s.requireAdmin(s.testnetOrder))
	mux.HandleFunc("GET /api/v1/testnet/orders", s.requireAdmin(s.testnetOrders))
	mux.HandleFunc("POST /api/v1/testnet/orders/{idempotency_key}/reconcile", s.requireAdmin(s.testnetReconcile))
	mux.HandleFunc("GET /metrics", s.metricsEndpoint)
	return s.recoverPanic(s.logRequests(mux))
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "работает"})
}

func (s *Server) ready(writer http.ResponseWriter, request *http.Request) {
	state, err := s.safety.State(request.Context())
	if err != nil {
		s.metrics.SetSafetyUnavailable()
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "safety-store недоступен"})
		return
	}
	s.observeSafetyState(state)
	writeJSON(writer, http.StatusOK, map[string]string{"status": "готов"})
}

func (s *Server) status(writer http.ResponseWriter, request *http.Request) {
	safetyState, err := s.safety.State(request.Context())
	storageAvailable := err == nil
	if err != nil {
		s.metrics.SetSafetyUnavailable()
		safetyState.Active = true
	} else {
		s.observeSafetyState(safetyState)
	}
	paperEnabled := s.paper != nil && s.paper.Enabled()
	testnetEnabled := s.testExchange != nil && s.testExchange.Enabled()
	mode := "исследование и бэктест"
	if paperEnabled {
		mode = "исследование, бэктест и paper-trading"
	}
	if testnetEnabled {
		mode += " и Binance Spot Testnet"
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"service": "TradingMaster", "version": s.version,
		"mode": mode, "live_trading": false,
		"kill_switch_active":     safetyState.Active,
		"durable_safety_store":   s.safety.Durable(),
		"safety_store_available": storageAvailable,
		"safety_admin_enabled":   s.adminToken != "",
		"telegram_enabled":       s.notifier.Enabled(),
		"paper_trading":          paperEnabled,
		"test_exchange_enabled":  testnetEnabled,
		"test_exchange":          "Binance Spot Testnet",
		"test_exchange_order_mode": func() string {
			if !testnetEnabled {
				return "disabled"
			}
			return string(s.testExchange.Mode())
		}(),
	})
}

func (s *Server) testnetStatus(writer http.ResponseWriter, request *http.Request) {
	if !s.testnetEnabled(writer) {
		return
	}
	status, err := s.testExchange.Status(request.Context())
	if err != nil {
		s.logger.Error("Binance Spot Testnet недоступен", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "тестовая биржа недоступна"})
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func (s *Server) testnetAccount(writer http.ResponseWriter, request *http.Request) {
	if !s.testnetEnabled(writer) {
		return
	}
	account, err := s.testExchange.Account(request.Context())
	if err != nil {
		s.logger.Error("счёт Binance Spot Testnet не прочитан", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "счёт тестовой биржи недоступен"})
		return
	}
	writeJSON(writer, http.StatusOK, account)
}

func (s *Server) testnetOrder(writer http.ResponseWriter, request *http.Request) {
	if !s.testnetEnabled(writer) {
		return
	}
	var payload testexchange.SubmitRequest
	if err := decodeStrictJSON(writer, request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "некорректная заявка тестовой биржи", err)
		return
	}
	order, err := s.testExchange.Submit(request.Context(), payload)
	if errors.Is(err, testexchange.ErrInvalidRequest) {
		writeError(writer, http.StatusUnprocessableEntity, "заявка тестовой биржи отклонена", err)
		return
	}
	if errors.Is(err, testexchange.ErrIdempotencyConflict) {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		s.logger.Error("заявка тестовой биржи не обработана", "ошибка", err)
		s.metrics.ObserveTestnetOrder("error")
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "адаптер тестовой биржи недоступен"})
		return
	}
	s.metrics.ObserveTestnetOrder(order.Status)
	status := http.StatusCreated
	if order.Replayed {
		status = http.StatusOK
	} else if order.Status == testexchange.StatusUnknown {
		status = http.StatusAccepted
	} else if order.Status == testexchange.StatusRejected {
		status = http.StatusConflict
	}
	writeJSON(writer, status, map[string]any{"order": order})
}

func (s *Server) testnetOrders(writer http.ResponseWriter, request *http.Request) {
	if !s.testnetEnabled(writer) {
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "limit должен быть от 1 до 200"})
			return
		}
		limit = parsed
	}
	orders, err := s.testExchange.Orders(request.Context(), limit)
	if err != nil {
		s.logger.Error("журнал тестовой биржи не прочитан", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "журнал тестовой биржи недоступен"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"orders": orders})
}

func (s *Server) testnetReconcile(writer http.ResponseWriter, request *http.Request) {
	if !s.testnetEnabled(writer) {
		return
	}
	order, err := s.testExchange.Reconcile(request.Context(), request.PathValue("idempotency_key"))
	if errors.Is(err, testexchange.ErrInvalidRequest) {
		writeError(writer, http.StatusUnprocessableEntity, "ключ идемпотентности отклонён", err)
		return
	}
	if errors.Is(err, testexchange.ErrOrderNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		s.logger.Error("сверка заявки тестовой биржи не выполнена", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "сверка с тестовой биржей недоступна"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"order": order})
}

func (s *Server) testnetEnabled(writer http.ResponseWriter) bool {
	if s.testExchange == nil || !s.testExchange.Enabled() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{
			"error": "тестовая биржа отключена: настройте durable PostgreSQL и тестовые API-ключи",
		})
		return false
	}
	return true
}

func (s *Server) metricsEndpoint(writer http.ResponseWriter, request *http.Request) {
	state, err := s.safety.State(request.Context())
	if err != nil {
		s.metrics.SetSafetyUnavailable()
	} else {
		s.observeSafetyState(state)
	}
	if s.paper != nil && s.paper.Enabled() {
		if portfolio, portfolioErr := s.paper.Portfolio(request.Context()); portfolioErr == nil {
			s.metrics.SetPaperEquity(portfolio.Equity)
		}
	}
	s.metrics.ServeHTTP(writer, request)
}

func (s *Server) safetyStatus(writer http.ResponseWriter, request *http.Request) {
	state, err := s.safety.State(request.Context())
	if err != nil {
		s.metrics.SetSafetyUnavailable()
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "safety-store недоступен"})
		return
	}
	writeJSON(writer, http.StatusOK, state)
}

func (s *Server) killSwitch(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := decodeStrictJSON(writer, request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "некорректная команда kill switch", err)
		return
	}
	now := time.Now().UTC()
	var (
		state      safety.State
		transition safety.Transition
		err        error
	)
	switch payload.Action {
	case "activate":
		if strings.TrimSpace(payload.Reason) == "" {
			writeJSON(writer, http.StatusUnprocessableEntity, map[string]string{
				"error": "для активации обязательна причина",
			})
			return
		}
		state, transition, err = s.safety.Activate(request.Context(), payload.Reason, now)
	case "deactivate":
		state, transition, err = s.safety.Deactivate(request.Context(), now)
	default:
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]string{
			"error": "action должен быть activate или deactivate",
		})
		return
	}
	if err != nil {
		s.metrics.SetSafetyUnavailable()
		s.logger.Error("команда kill switch не сохранена", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "safety-store недоступен"})
		return
	}
	s.observeSafetyState(state)
	s.notifyTransition(transition)
	writeJSON(writer, http.StatusOK, state)
}

func (s *Server) observeEquity(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Equity     float64    `json:"equity"`
		ObservedAt *time.Time `json:"observed_at,omitempty"`
	}
	if err := decodeStrictJSON(writer, request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "некорректное значение equity", err)
		return
	}
	observedAt := time.Now().UTC()
	if payload.ObservedAt != nil {
		observedAt = payload.ObservedAt.UTC()
	}
	if observedAt.After(time.Now().UTC().Add(time.Minute)) {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]string{
			"error": "observed_at не может быть более чем на минуту в будущем",
		})
		return
	}
	if payload.Equity <= 0 {
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]string{"error": "equity должно быть положительным"})
		return
	}
	state, transition, err := s.safety.ObserveEquity(request.Context(), observedAt, payload.Equity)
	if err != nil {
		if errors.Is(err, safety.ErrStaleEquity) {
			writeError(writer, http.StatusConflict, "устаревшее equity не принято", err)
			return
		}
		s.metrics.SetSafetyUnavailable()
		s.logger.Error("equity не сохранено", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "safety-store недоступен"})
		return
	}
	s.observeSafetyState(state)
	s.notifyTransition(transition)
	writeJSON(writer, http.StatusOK, state)
}

func (s *Server) paperOrder(writer http.ResponseWriter, request *http.Request) {
	if !s.paperEnabled(writer) {
		return
	}
	var payload paper.SubmitRequest
	if err := decodeStrictJSON(writer, request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "некорректная paper-заявка", err)
		return
	}
	order, err := s.paper.Submit(request.Context(), payload)
	if errors.Is(err, paper.ErrInvalidRequest) {
		writeError(writer, http.StatusUnprocessableEntity, "paper-заявка отклонена", err)
		return
	}
	if err != nil {
		s.logger.Error("paper-заявка не обработана", "ошибка", err)
		s.metrics.ObservePaperOrder("error")
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "paper broker недоступен"})
		return
	}
	s.metrics.ObservePaperOrder(order.Status)
	if order.SafetyState != nil {
		s.observeSafetyState(*order.SafetyState)
	}
	if order.SafetyTransition != nil {
		s.notifyTransition(*order.SafetyTransition)
	}
	response := map[string]any{"order": order}
	portfolio, portfolioErr := s.paper.Portfolio(request.Context())
	if portfolioErr == nil {
		s.metrics.SetPaperEquity(portfolio.Equity)
		response["portfolio"] = portfolio
	} else {
		s.logger.Error("paper-заявка записана, но портфель не прочитан", "ошибка", portfolioErr)
		response["warning"] = "заявка записана, но актуальный портфель не удалось прочитать"
	}
	status := http.StatusCreated
	if order.Status == paper.StatusRejected {
		status = http.StatusConflict
	}
	writeJSON(writer, status, response)
}

func (s *Server) paperOrders(writer http.ResponseWriter, request *http.Request) {
	if !s.paperEnabled(writer) {
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "limit должен быть от 1 до 200"})
			return
		}
		limit = parsed
	}
	orders, err := s.paper.Orders(request.Context(), limit)
	if err != nil {
		s.logger.Error("не удалось прочитать журнал paper-заявок", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "paper broker недоступен"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"orders": orders})
}

func (s *Server) paperPortfolio(writer http.ResponseWriter, request *http.Request) {
	if !s.paperEnabled(writer) {
		return
	}
	portfolio, err := s.paper.Portfolio(request.Context())
	if err != nil {
		s.logger.Error("не удалось прочитать paper-портфель", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "paper broker недоступен"})
		return
	}
	s.metrics.SetPaperEquity(portfolio.Equity)
	writeJSON(writer, http.StatusOK, portfolio)
}

func (s *Server) paperMark(writer http.ResponseWriter, request *http.Request) {
	if !s.paperEnabled(writer) {
		return
	}
	var payload paper.MarkRequest
	if err := decodeStrictJSON(writer, request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "некорректная рыночная цена", err)
		return
	}
	result, err := s.paper.Mark(request.Context(), payload)
	if err != nil {
		if errors.Is(err, paper.ErrInvalidRequest) {
			writeError(writer, http.StatusUnprocessableEntity, "рыночная цена отклонена", err)
			return
		}
		s.logger.Error("не удалось обновить paper-цену", "ошибка", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "paper broker недоступен"})
		return
	}
	s.metrics.SetPaperEquity(result.Portfolio.Equity)
	s.observeSafetyState(result.SafetyState)
	s.notifyTransition(result.Transition)
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) paperEnabled(writer http.ResponseWriter) bool {
	if s.paper == nil || !s.paper.Enabled() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{
			"error": "paper trading отключён: durable PostgreSQL не настроен",
		})
		return false
	}
	return true
}

func (s *Server) backtest(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	var observedErr error
	defer func() { s.metrics.ObserveBacktest(time.Since(started), observedErr) }()

	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var payload BacktestRequest
	if err := decoder.Decode(&payload); err != nil {
		observedErr = err
		writeError(writer, http.StatusBadRequest, "некорректное тело запроса", err)
		return
	}
	if payload.Strategy == (strategy.Config{}) {
		payload.Strategy = strategy.DefaultConfig()
	}
	if payload.Risk == (risk.Config{}) {
		payload.Risk = risk.DefaultConfig()
	}
	if payload.Execution == (backtest.Config{}) {
		payload.Execution = backtest.DefaultConfig()
	}

	strategyEngine, err := strategy.NewTrendBreakout(payload.Strategy)
	if err != nil {
		observedErr = err
		writeError(writer, http.StatusUnprocessableEntity, "некорректная стратегия", err)
		return
	}
	riskManager, err := risk.NewManager(payload.Risk, payload.Execution.InitialCapital)
	if err != nil {
		observedErr = err
		writeError(writer, http.StatusUnprocessableEntity, "некорректный риск-менеджмент", err)
		return
	}
	engine, err := backtest.NewEngine(payload.Execution, strategyEngine, riskManager)
	if err != nil {
		observedErr = err
		writeError(writer, http.StatusUnprocessableEntity, "некорректная конфигурация исполнения", err)
		return
	}
	result, err := engine.Run(payload.Candles)
	if err != nil {
		observedErr = err
		writeError(writer, http.StatusUnprocessableEntity, "бэктест не выполнен", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(writer, request)
		s.logger.Info("HTTP-запрос", "метод", request.Method, "путь", request.URL.Path, "длительность", time.Since(started))
	})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if s.adminToken == "" {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{
				"error": "административный API безопасности отключён: TM_ADMIN_TOKEN не настроен",
			})
			return
		}
		const prefix = "Bearer "
		authorization := request.Header.Get("Authorization")
		providedToken, found := strings.CutPrefix(authorization, prefix)
		providedHash := sha256.Sum256([]byte(providedToken))
		expectedHash := sha256.Sum256([]byte(s.adminToken))
		if !found || subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) != 1 {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "требуется корректный admin token"})
			return
		}
		next(writer, request)
	}
}

func (s *Server) observeSafetyState(state safety.State) {
	s.metrics.SetSafety(state.Active, state.DailyLossRatio, state.DailyLossLimit)
}

func (s *Server) notifyTransition(transition safety.Transition) {
	if !transition.Changed || !s.notifier.Enabled() {
		return
	}
	severity := alert.SeverityInfo
	title := "Kill switch снят"
	if transition.Engaged {
		severity = alert.SeverityCritical
		title = "Kill switch активирован"
	} else if transition.Active {
		severity = alert.SeverityWarning
		title = "Часть защитных блокировок снята"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	err := s.notifier.Notify(ctx, alert.Event{
		Severity: severity, Title: title, Message: transition.Cause, Time: transition.Time,
	})
	s.metrics.ObserveAlert(err)
	if err != nil {
		s.logger.Error("не удалось отправить Telegram-уведомление", "ошибка", err)
	}
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("паника HTTP-обработчика", "ошибка", recovered)
				writeError(writer, http.StatusInternalServerError, "внутренняя ошибка", errors.New("обработчик аварийно завершился"))
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

func writeError(writer http.ResponseWriter, status int, message string, err error) {
	writeJSON(writer, status, map[string]string{"error": message, "details": err.Error()})
}

func decodeStrictJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("разрешён только один JSON-объект")
		}
		return err
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		_, _ = fmt.Fprintln(writer, `{"error":"не удалось сформировать JSON"}`)
	}
}
