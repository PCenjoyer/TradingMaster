package backtest

import (
	"testing"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/PCenjoyer/TradingMaster/internal/risk"
)

type scheduledStrategy struct{}

func (scheduledStrategy) Evaluate(candles []domain.Candle, _ bool) domain.Signal {
	switch len(candles) {
	case 2:
		return domain.Signal{Action: domain.SignalBuy, Reason: "тестовый вход", ATR: 1}
	case 4:
		return domain.Signal{Action: domain.SignalSell, Reason: "тестовый выход", ATR: 1}
	default:
		return domain.Signal{Action: domain.SignalHold, ATR: 1}
	}
}

func TestEngineExecutesSignalsOnNextBar(t *testing.T) {
	execution := Config{InitialCapital: 1_000, FeeBPS: 10, SlippageBPS: 0}
	riskManager, err := risk.NewManager(risk.Config{
		RiskPerTrade: 0.01, StopATRMultiplier: 2, MaxPositionFraction: 0.5,
		MaxDrawdown: 0.2, MinOrderValue: 1,
	}, execution.InitialCapital)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(execution, scheduledStrategy{}, riskManager)
	if err != nil {
		t.Fatal(err)
	}
	candles := []domain.Candle{
		testCandle(0, 10, 11, 9.5, 10.5),
		testCandle(1, 10.5, 11, 10, 10.8),
		testCandle(2, 11, 12, 10.5, 11.8),
		testCandle(3, 12, 13, 11.5, 12.8),
		testCandle(4, 13, 14, 12.5, 13.8),
	}

	result, err := engine.Run(candles)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 1 {
		t.Fatalf("ожидалась 1 сделка, получено %d", len(result.Trades))
	}
	trade := result.Trades[0]
	if !trade.EntryTime.Equal(candles[2].Time) {
		t.Fatalf("вход должен быть на следующей свече: %s", trade.EntryTime)
	}
	if !trade.ExitTime.Equal(candles[4].Time) {
		t.Fatalf("выход должен быть на следующей свече: %s", trade.ExitTime)
	}
	if trade.PnL <= 0 || result.Statistics.FinalEquity <= execution.InitialCapital {
		t.Fatalf("ожидалась прибыльная сделка: pnl=%f equity=%f", trade.PnL, result.Statistics.FinalEquity)
	}
}

func TestEngineRejectsUnorderedCandles(t *testing.T) {
	execution := DefaultConfig()
	riskManager, err := risk.NewManager(risk.DefaultConfig(), execution.InitialCapital)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(execution, scheduledStrategy{}, riskManager)
	if err != nil {
		t.Fatal(err)
	}
	candles := []domain.Candle{
		testCandle(0, 10, 11, 9, 10),
		testCandle(1, 10, 11, 9, 10),
		testCandle(1, 10, 11, 9, 10),
	}
	if _, err := engine.Run(candles); err == nil {
		t.Fatal("ожидалась ошибка порядка свечей")
	}
}

func testCandle(day int, open, high, low, closePrice float64) domain.Candle {
	return domain.Candle{
		Time: time.Date(2025, time.January, day+1, 0, 0, 0, 0, time.UTC),
		Open: open, High: high, Low: low, Close: closePrice, Volume: 100,
	}
}
