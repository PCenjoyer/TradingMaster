package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/backtest"
	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/PCenjoyer/TradingMaster/internal/observability"
	"github.com/PCenjoyer/TradingMaster/internal/risk"
	"github.com/PCenjoyer/TradingMaster/internal/strategy"
)

const maxRequestSize = 16 << 20

type BacktestRequest struct {
	Candles   []domain.Candle `json:"candles"`
	Strategy  strategy.Config `json:"strategy"`
	Risk      risk.Config     `json:"risk"`
	Execution backtest.Config `json:"execution"`
}

type Server struct {
	logger  *slog.Logger
	metrics *observability.Metrics
	version string
}

func NewServer(logger *slog.Logger, version string) *Server {
	return &Server{logger: logger, metrics: &observability.Metrics{}, version: version}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.health)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("POST /api/v1/backtests", s.backtest)
	mux.Handle("GET /metrics", s.metrics)
	return s.recoverPanic(s.logRequests(mux))
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "работает"})
}

func (s *Server) status(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"service": "TradingMaster", "version": s.version,
		"mode": "исследование и бэктест", "live_trading": false,
	})
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

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		_, _ = fmt.Fprintln(writer, `{"error":"не удалось сформировать JSON"}`)
	}
}
