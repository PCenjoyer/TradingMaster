package risk

import (
	"fmt"
	"math"
)

type Config struct {
	RiskPerTrade        float64 `json:"risk_per_trade"`
	StopATRMultiplier   float64 `json:"stop_atr_multiplier"`
	MaxPositionFraction float64 `json:"max_position_fraction"`
	MaxDrawdown         float64 `json:"max_drawdown"`
	MinOrderValue       float64 `json:"min_order_value"`
}

func DefaultConfig() Config {
	return Config{
		RiskPerTrade:        0.01,
		StopATRMultiplier:   2,
		MaxPositionFraction: 0.25,
		MaxDrawdown:         0.15,
		MinOrderValue:       10,
	}
}

type Manager struct {
	config     Config
	peakEquity float64
}

func NewManager(config Config, initialEquity float64) (*Manager, error) {
	if initialEquity <= 0 {
		return nil, fmt.Errorf("начальный капитал должен быть положительным")
	}
	if config.RiskPerTrade <= 0 || config.RiskPerTrade > 0.05 {
		return nil, fmt.Errorf("риск на сделку должен быть в диапазоне (0; 0.05]")
	}
	if config.StopATRMultiplier <= 0 || config.MaxPositionFraction <= 0 || config.MaxPositionFraction > 1 {
		return nil, fmt.Errorf("некорректные ограничения позиции")
	}
	if config.MaxDrawdown <= 0 || config.MaxDrawdown >= 1 || config.MinOrderValue < 0 {
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

func (m *Manager) UpdateEquity(equity float64) {
	if equity > m.peakEquity {
		m.peakEquity = equity
	}
}

func (m *Manager) Halted(equity float64) bool {
	return equity <= m.peakEquity*(1-m.config.MaxDrawdown)
}
