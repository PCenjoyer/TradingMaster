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
	"strings"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/alert"
	"github.com/PCenjoyer/TradingMaster/internal/backtest"
	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/PCenjoyer/TradingMaster/internal/observability"
	"github.com/PCenjoyer/TradingMaster/internal/risk"
	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/PCenjoyer/TradingMaster/internal/strategy"
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
}

func DefaultConfig() Config {
	return Config{DailyLossLimit: 0.03, Notifier: alert.Noop{}}
}

type Server struct {
	logger     *slog.Logger
	metrics    *observability.Metrics
	safety     *safety.Controller
	notifier   alert.Notifier
	adminToken string
	version    string
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
	controller, err := safety.NewController(safety.Config{
		DailyLossLimit: config.DailyLossLimit,
		InitialActive:  config.InitialKillSwitch,
		InitialReason:  "безопасная блокировка при запуске сервиса",
	})
	if err != nil {
		return nil, err
	}
	metrics := &observability.Metrics{}
	state := controller.State()
	metrics.SetSafety(state.Active, state.DailyLossRatio, state.DailyLossLimit)
	server := &Server{
		logger: logger, metrics: metrics, safety: controller, notifier: config.Notifier,
		adminToken: config.AdminToken, version: version,
	}
	if state.Active {
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
	mux.HandleFunc("GET /readyz", s.health)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("POST /api/v1/backtests", s.backtest)
	mux.HandleFunc("GET /api/v1/safety", s.requireAdmin(s.safetyStatus))
	mux.HandleFunc("POST /api/v1/safety/kill-switch", s.requireAdmin(s.killSwitch))
	mux.HandleFunc("POST /api/v1/safety/equity", s.requireAdmin(s.observeEquity))
	mux.Handle("GET /metrics", s.metrics)
	return s.recoverPanic(s.logRequests(mux))
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "работает"})
}

func (s *Server) status(writer http.ResponseWriter, _ *http.Request) {
	safetyState := s.safety.State()
	writeJSON(writer, http.StatusOK, map[string]any{
		"service": "TradingMaster", "version": s.version,
		"mode": "исследование и бэктест", "live_trading": false,
		"kill_switch_active":   safetyState.Active,
		"safety_admin_enabled": s.adminToken != "",
		"telegram_enabled":     s.notifier.Enabled(),
	})
}

func (s *Server) safetyStatus(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, s.safety.State())
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
		state, transition, err = s.safety.Activate(payload.Reason, now)
	case "deactivate":
		state, transition = s.safety.Deactivate(now)
	default:
		err = fmt.Errorf("action должен быть activate или deactivate")
	}
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "команда kill switch отклонена", err)
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
	state, transition, err := s.safety.ObserveEquity(observedAt, payload.Equity)
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "equity не принято", err)
		return
	}
	s.observeSafetyState(state)
	s.notifyTransition(transition)
	writeJSON(writer, http.StatusOK, state)
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
