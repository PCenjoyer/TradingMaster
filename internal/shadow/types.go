package shadow

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/PCenjoyer/TradingMaster/internal/strategy"
)

const (
	liveMarketRESTURL   = "https://data-api.binance.vision"
	liveMarketStreamURL = "wss://stream.binance.com:9443"
)

var shadowSymbolPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,31}$`)

var supportedIntervals = map[string]time.Duration{
	"1m": time.Minute, "3m": 3 * time.Minute, "5m": 5 * time.Minute,
	"15m": 15 * time.Minute, "30m": 30 * time.Minute, "1h": time.Hour,
	"4h": 4 * time.Hour, "1d": 24 * time.Hour,
}

type Config struct {
	Symbols       []string
	Interval      string
	HistoryLimit  int
	Strategy      strategy.Config
	ReconnectMin  time.Duration
	ReconnectMax  time.Duration
	HeartbeatRate time.Duration
}

func DefaultConfig() Config {
	return Config{
		Symbols: []string{"BTCUSDT"}, Interval: "1m", HistoryLimit: 120,
		Strategy: strategy.DefaultConfig(), ReconnectMin: time.Second,
		ReconnectMax: 30 * time.Second, HeartbeatRate: 5 * time.Second,
	}
}

func (config Config) normalized() (Config, error) {
	defaults := DefaultConfig()
	if config.Interval == "" {
		config.Interval = defaults.Interval
	}
	if config.HistoryLimit == 0 {
		config.HistoryLimit = defaults.HistoryLimit
	}
	if config.Strategy == (strategy.Config{}) {
		config.Strategy = defaults.Strategy
	}
	if config.ReconnectMin == 0 {
		config.ReconnectMin = defaults.ReconnectMin
	}
	if config.ReconnectMax == 0 {
		config.ReconnectMax = defaults.ReconnectMax
	}
	if config.HeartbeatRate == 0 {
		config.HeartbeatRate = defaults.HeartbeatRate
	}
	if len(config.Symbols) == 0 || len(config.Symbols) > 10 {
		return Config{}, fmt.Errorf("shadow-режим требует от 1 до 10 символов")
	}
	seen := make(map[string]struct{}, len(config.Symbols))
	normalizedSymbols := make([]string, 0, len(config.Symbols))
	for _, raw := range config.Symbols {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if !shadowSymbolPattern.MatchString(symbol) {
			return Config{}, fmt.Errorf("некорректный shadow-символ %q", raw)
		}
		if _, exists := seen[symbol]; exists {
			continue
		}
		seen[symbol] = struct{}{}
		normalizedSymbols = append(normalizedSymbols, symbol)
	}
	if len(normalizedSymbols) == 0 {
		return Config{}, fmt.Errorf("список shadow-символов пуст")
	}
	sort.Strings(normalizedSymbols)
	config.Symbols = normalizedSymbols
	if _, ok := supportedIntervals[config.Interval]; !ok {
		return Config{}, fmt.Errorf("неподдерживаемый shadow-интервал %q", config.Interval)
	}
	if config.HistoryLimit < 60 || config.HistoryLimit > 1_000 {
		return Config{}, fmt.Errorf("shadow history limit должен быть от 60 до 1000")
	}
	if config.ReconnectMin <= 0 || config.ReconnectMax < config.ReconnectMin || config.ReconnectMax > 5*time.Minute {
		return Config{}, fmt.Errorf("некорректные интервалы переподключения shadow-потока")
	}
	if config.HeartbeatRate < time.Second || config.HeartbeatRate > time.Minute {
		return Config{}, fmt.Errorf("shadow heartbeat должен быть от 1 секунды до 1 минуты")
	}
	if _, err := strategy.NewTrendBreakout(config.Strategy); err != nil {
		return Config{}, fmt.Errorf("настроить shadow-стратегию: %w", err)
	}
	return config, nil
}

type MarketCandle struct {
	Symbol    string        `json:"symbol"`
	Interval  string        `json:"interval"`
	Candle    domain.Candle `json:"candle"`
	CloseTime time.Time     `json:"close_time"`
}

type Signal struct {
	ID               int64               `json:"id"`
	Symbol           string              `json:"symbol"`
	Interval         string              `json:"interval"`
	CandleTime       time.Time           `json:"candle_time"`
	ClosePrice       float64             `json:"close_price"`
	Action           domain.SignalAction `json:"action"`
	Reason           string              `json:"reason"`
	ATR              float64             `json:"atr"`
	InPositionBefore bool                `json:"in_position_before"`
	EligibleForOrder bool                `json:"eligible_for_order"`
	Blocked          bool                `json:"blocked"`
	BlockReason      string              `json:"block_reason,omitempty"`
	OrderSent        bool                `json:"order_sent"`
	CreatedAt        time.Time           `json:"created_at"`
}

type Status struct {
	Enabled          bool       `json:"enabled"`
	Mode             string     `json:"mode"`
	MarketDataSource string     `json:"market_data_source"`
	Symbols          []string   `json:"symbols"`
	Interval         string     `json:"interval"`
	Leader           bool       `json:"leader"`
	Connected        bool       `json:"connected"`
	LastEventAt      *time.Time `json:"last_event_at,omitempty"`
	LastCandleAt     *time.Time `json:"last_candle_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	Reconnects       int64      `json:"reconnects"`
	CandlesProcessed int64      `json:"candles_processed"`
	SignalsGenerated int64      `json:"signals_generated"`
	OrdersSent       int64      `json:"orders_sent"`
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
}

type Service interface {
	Enabled() bool
	Run(context.Context) error
	Status(context.Context) (Status, error)
	Signals(context.Context, int) ([]Signal, error)
	Candles(context.Context, string, int) ([]MarketCandle, error)
}
