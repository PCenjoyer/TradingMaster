package domain

import (
	"fmt"
	"time"
)

// Candle содержит одну завершённую свечу OHLCV.
type Candle struct {
	Time   time.Time `json:"time"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}

func (c Candle) Validate() error {
	if c.Time.IsZero() {
		return fmt.Errorf("не указано время свечи")
	}
	if c.Open <= 0 || c.High <= 0 || c.Low <= 0 || c.Close <= 0 {
		return fmt.Errorf("цены должны быть положительными")
	}
	if c.High < c.Low || c.High < c.Open || c.High < c.Close || c.Low > c.Open || c.Low > c.Close {
		return fmt.Errorf("нарушена геометрия OHLC")
	}
	if c.Volume < 0 {
		return fmt.Errorf("объём не может быть отрицательным")
	}
	return nil
}

type SignalAction string

const (
	SignalHold SignalAction = "hold"
	SignalBuy  SignalAction = "buy"
	SignalSell SignalAction = "sell"
)

type Signal struct {
	Action SignalAction `json:"action"`
	Reason string       `json:"reason"`
	ATR    float64      `json:"atr"`
}

type Position struct {
	EntryTime  time.Time
	EntryPrice float64
	Quantity   float64
	StopPrice  float64
	EntryFee   float64
}

type Trade struct {
	EntryTime  time.Time `json:"entry_time"`
	ExitTime   time.Time `json:"exit_time"`
	EntryPrice float64   `json:"entry_price"`
	ExitPrice  float64   `json:"exit_price"`
	Quantity   float64   `json:"quantity"`
	Fees       float64   `json:"fees"`
	PnL        float64   `json:"pnl"`
	PnLPercent float64   `json:"pnl_percent"`
	ExitReason string    `json:"exit_reason"`
}

type EquityPoint struct {
	Time   time.Time `json:"time"`
	Equity float64   `json:"equity"`
}

type Statistics struct {
	InitialCapital   float64 `json:"initial_capital"`
	FinalEquity      float64 `json:"final_equity"`
	TotalReturn      float64 `json:"total_return"`
	BuyAndHold       float64 `json:"buy_and_hold"`
	MaxDrawdown      float64 `json:"max_drawdown"`
	Sharpe           float64 `json:"sharpe"`
	ProfitFactor     float64 `json:"profit_factor"`
	WinRate          float64 `json:"win_rate"`
	Exposure         float64 `json:"exposure"`
	Trades           int     `json:"trades"`
	ProfitableTrades int     `json:"profitable_trades"`
	DailyLossStops   int     `json:"daily_loss_stops"`
}

type BacktestResult struct {
	Statistics  Statistics    `json:"statistics"`
	Trades      []Trade       `json:"trades"`
	EquityCurve []EquityPoint `json:"equity_curve"`
}
