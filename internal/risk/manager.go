package risk

import (
	"fmt"
	"math"
	"time"
)

type Config struct {
	RiskPerTrade        float64 `json:"risk_per_trade"`
	StopATRMultiplier   float64 `json:"stop_atr_multiplier"`
	MaxPositionFraction float64 `json:"max_position_fraction"`
	MaxDrawdown         float64 `json:"max_drawdown"`
	DailyLossLimit      float64 `json:"daily_loss_limit"`
	MinOrderValue       float64 `json:"min_order_value"`
}

func DefaultConfig() Config {
	return Config{
		RiskPerTrade:        0.01,
		StopATRMultiplier:   2,
		MaxPositionFraction: 0.25,
		MaxDrawdown:         0.15,
		DailyLossLimit:      0.03,
		MinOrderValue:       10,
	}
}

type Manager struct {
	config         Config
	peakEquity     float64
	tradingDay     string
	dayStartEquity float64
	dailyLossRatio float64
	dailyHalted    bool
	dailyStops     int
}

func NewManager(config Config, initialEquity float64) (*Manager, error) {
	if initialEquity <= 0 {
		return nil, fmt.Errorf("начальный капитал должен быть положительным")
	}
	if config.DailyLossLimit == 0 {
		config.DailyLossLimit = DefaultConfig().DailyLossLimit
	}
	if config.RiskPerTrade <= 0 || config.RiskPerTrade > 0.05 {
		return nil, fmt.Errorf("риск на сделку должен быть в диапазоне (0; 0.05]")
	}
	if config.StopATRMultiplier <= 0 || config.MaxPositionFraction <= 0 || config.MaxPositionFraction > 1 {
		return nil, fmt.Errorf("некорректные ограничения позиции")
	}
	if config.MaxDrawdown <= 0 || config.MaxDrawdown >= 1 || config.DailyLossLimit <= 0 || config.DailyLossLimit >= 1 || config.MinOrderValue < 0 {
		return nil, fmt.Errorf("некорректные защитные ограничения")
	}
	return &Manager{config: config, peakEquity: initialEquity}, nil
}

func (m *Manager) PositionSize(equity, price, atr, availableCash, feeRate float64) float64 {
	if m.Halted(equity) || price <= 0 || atr <= 0 || availableCash <= 0 {
		return 0
	}
	riskBudget := equity * m.config.RiskPerTrade
	byRisk := riskBudget / (atr * m.config.StopATRMultiplier)
	byExposure := equity * m.config.MaxPositionFraction / price
	byCash := availableCash / (price * (1 + feeRate))
	quantity := math.Min(byRisk, math.Min(byExposure, byCash))
	if quantity*price < m.config.MinOrderValue {
		return 0
	}
	return quantity
}

func (m *Manager) StopPrice(entryPrice, atr float64) float64 {
	return math.Max(0, entryPrice-atr*m.config.StopATRMultiplier)
}

func (m *Manager) TrailingStop(closePrice, atr float64) float64 {
	return math.Max(0, closePrice-atr*m.config.StopATRMultiplier)
}

func (m *Manager) UpdateEquity(at time.Time, equity float64) {
	if equity > m.peakEquity {
		m.peakEquity = equity
	}
	day := at.UTC().Format(time.DateOnly)
	if m.tradingDay != day {
		m.tradingDay = day
		m.dayStartEquity = equity
		m.dailyLossRatio = 0
		m.dailyHalted = false
		return
	}
	m.dailyLossRatio = math.Max(0, (m.dayStartEquity-equity)/m.dayStartEquity)
	if !m.dailyHalted && m.dailyLossRatio >= m.config.DailyLossLimit {
		m.dailyHalted = true
		m.dailyStops++
	}
}

func (m *Manager) Halted(equity float64) bool {
	return m.dailyHalted || equity <= m.peakEquity*(1-m.config.MaxDrawdown)
}

func (m *Manager) HaltReason(equity float64) string {
	if m.dailyHalted {
		return "превышен дневной лимит убытка"
	}
	if equity <= m.peakEquity*(1-m.config.MaxDrawdown) {
		return "превышена максимальная просадка"
	}
	return ""
}

func (m *Manager) DailyLossStops() int {
	return m.dailyStops
}
