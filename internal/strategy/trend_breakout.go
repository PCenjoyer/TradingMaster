package strategy

import (
	"fmt"
	"math"

	"github.com/PCenjoyer/TradingMaster/internal/domain"
)

type Config struct {
	EntryPeriod int `json:"entry_period"`
	ExitPeriod  int `json:"exit_period"`
	ATRPeriod   int `json:"atr_period"`
	TrendPeriod int `json:"trend_period"`
}

func DefaultConfig() Config {
	return Config{EntryPeriod: 20, ExitPeriod: 10, ATRPeriod: 14, TrendPeriod: 50}
}

type TrendBreakout struct {
	config Config
}

func NewTrendBreakout(config Config) (*TrendBreakout, error) {
	if config.EntryPeriod < 2 || config.ExitPeriod < 2 || config.ATRPeriod < 2 || config.TrendPeriod < 2 {
		return nil, fmt.Errorf("все периоды стратегии должны быть не меньше 2")
	}
	return &TrendBreakout{config: config}, nil
}

func (s *TrendBreakout) Evaluate(candles []domain.Candle, inPosition bool) domain.Signal {
	needed := max(s.config.EntryPeriod+1, s.config.ExitPeriod+1, s.config.ATRPeriod+1, s.config.TrendPeriod)
	if len(candles) < needed {
		return domain.Signal{Action: domain.SignalHold, Reason: "недостаточно истории"}
	}

	current := candles[len(candles)-1]
	atr := averageTrueRange(candles, s.config.ATRPeriod)
	if atr <= 0 || math.IsNaN(atr) || math.IsInf(atr, 0) {
		return domain.Signal{Action: domain.SignalHold, Reason: "ATR недоступен"}
	}

	if inPosition {
		lowest := lowestLow(candles[len(candles)-1-s.config.ExitPeriod : len(candles)-1])
		if current.Close < lowest {
			return domain.Signal{Action: domain.SignalSell, Reason: "пробой нижнего канала", ATR: atr}
		}
		return domain.Signal{Action: domain.SignalHold, Reason: "позиция удерживается", ATR: atr}
	}

	highest := highestHigh(candles[len(candles)-1-s.config.EntryPeriod : len(candles)-1])
	trend := simpleMovingAverage(candles[len(candles)-s.config.TrendPeriod:])
	if current.Close > highest && current.Close > trend {
		return domain.Signal{Action: domain.SignalBuy, Reason: "пробой верхнего канала по тренду", ATR: atr}
	}
	return domain.Signal{Action: domain.SignalHold, Reason: "условий для входа нет", ATR: atr}
}

func averageTrueRange(candles []domain.Candle, period int) float64 {
	start := len(candles) - period
	var sum float64
	for i := start; i < len(candles); i++ {
		previousClose := candles[i-1].Close
		trueRange := math.Max(candles[i].High-candles[i].Low, math.Max(math.Abs(candles[i].High-previousClose), math.Abs(candles[i].Low-previousClose)))
		sum += trueRange
	}
	return sum / float64(period)
}

func highestHigh(candles []domain.Candle) float64 {
	value := candles[0].High
	for _, candle := range candles[1:] {
		value = math.Max(value, candle.High)
	}
	return value
}

func lowestLow(candles []domain.Candle) float64 {
	value := candles[0].Low
	for _, candle := range candles[1:] {
		value = math.Min(value, candle.Low)
	}
	return value
}

func simpleMovingAverage(candles []domain.Candle) float64 {
	var sum float64
	for _, candle := range candles {
		sum += candle.Close
	}
	return sum / float64(len(candles))
}
