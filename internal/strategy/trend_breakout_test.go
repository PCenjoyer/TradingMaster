package strategy

import (
	"testing"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/domain"
)

func TestTrendBreakoutBuysOnlyAfterPriorChannelBreak(t *testing.T) {
	engine, err := NewTrendBreakout(Config{EntryPeriod: 3, ExitPeriod: 2, ATRPeriod: 2, TrendPeriod: 3})
	if err != nil {
		t.Fatal(err)
	}
	candles := []domain.Candle{
		candle(0, 10, 11, 9, 10),
		candle(1, 10, 12, 10, 11),
		candle(2, 11, 13, 11, 12),
		candle(3, 12, 15, 12, 14),
	}

	signal := engine.Evaluate(candles, false)
	if signal.Action != domain.SignalBuy {
		t.Fatalf("ожидался сигнал buy, получен %s (%s)", signal.Action, signal.Reason)
	}
	if signal.ATR <= 0 {
		t.Fatalf("ожидался положительный ATR, получен %f", signal.ATR)
	}
}

func TestTrendBreakoutExitsPositionOnLowerChannelBreak(t *testing.T) {
	engine, err := NewTrendBreakout(Config{EntryPeriod: 3, ExitPeriod: 2, ATRPeriod: 2, TrendPeriod: 3})
	if err != nil {
		t.Fatal(err)
	}
	candles := []domain.Candle{
		candle(0, 10, 11, 9, 10),
		candle(1, 10, 12, 10, 11),
		candle(2, 11, 13, 9, 10),
		candle(3, 9, 10, 7, 8),
	}

	signal := engine.Evaluate(candles, true)
	if signal.Action != domain.SignalSell {
		t.Fatalf("ожидался сигнал sell, получен %s (%s)", signal.Action, signal.Reason)
	}
}

func candle(day int, open, high, low, closePrice float64) domain.Candle {
	return domain.Candle{
		Time: time.Date(2025, time.January, day+1, 0, 0, 0, 0, time.UTC),
		Open: open, High: high, Low: low, Close: closePrice, Volume: 100,
	}
}
