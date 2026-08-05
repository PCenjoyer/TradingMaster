package backtest

import (
	"fmt"
	"math"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/PCenjoyer/TradingMaster/internal/risk"
)

type Strategy interface {
	Evaluate(candles []domain.Candle, inPosition bool) domain.Signal
}

type Config struct {
	InitialCapital float64 `json:"initial_capital"`
	FeeBPS         float64 `json:"fee_bps"`
	SlippageBPS    float64 `json:"slippage_bps"`
}

func DefaultConfig() Config {
	return Config{InitialCapital: 10_000, FeeBPS: 10, SlippageBPS: 5}
}

type Engine struct {
	config   Config
	strategy Strategy
	risk     *risk.Manager
}

func NewEngine(config Config, strategy Strategy, riskManager *risk.Manager) (*Engine, error) {
	if config.InitialCapital <= 0 {
		return nil, fmt.Errorf("начальный капитал должен быть положительным")
	}
	if config.FeeBPS < 0 || config.FeeBPS > 1_000 || config.SlippageBPS < 0 || config.SlippageBPS > 1_000 {
		return nil, fmt.Errorf("комиссия и проскальзывание должны быть в диапазоне [0; 1000] bps")
	}
	if strategy == nil || riskManager == nil {
		return nil, fmt.Errorf("стратегия и риск-менеджер обязательны")
	}
	return &Engine{config: config, strategy: strategy, risk: riskManager}, nil
}

func (e *Engine) Run(candles []domain.Candle) (domain.BacktestResult, error) {
	if len(candles) < 3 {
		return domain.BacktestResult{}, fmt.Errorf("для бэктеста нужно не менее трёх свечей")
	}
	for i, candle := range candles {
		if err := candle.Validate(); err != nil {
			return domain.BacktestResult{}, fmt.Errorf("свеча %d: %w", i, err)
		}
		if i > 0 && !candle.Time.After(candles[i-1].Time) {
			return domain.BacktestResult{}, fmt.Errorf("свечи должны быть строго упорядочены по времени")
		}
	}

	feeRate := e.config.FeeBPS / 10_000
	slippageRate := e.config.SlippageBPS / 10_000
	cash := e.config.InitialCapital
	var position *domain.Position
	pending := domain.Signal{Action: domain.SignalHold}
	trades := make([]domain.Trade, 0)
	curve := make([]domain.EquityPoint, 0, len(candles))
	exposedBars := 0

	for i, candle := range candles {
		if pending.Action == domain.SignalBuy && position == nil {
			fillPrice := candle.Open * (1 + slippageRate)
			quantity := e.risk.PositionSize(cash, fillPrice, pending.ATR, cash, feeRate)
			if quantity > 0 {
				fee := fillPrice * quantity * feeRate
				cash -= fillPrice*quantity + fee
				position = &domain.Position{
					EntryTime: candle.Time, EntryPrice: fillPrice, Quantity: quantity,
					StopPrice: e.risk.StopPrice(fillPrice, pending.ATR), EntryFee: fee,
				}
			}
		} else if pending.Action == domain.SignalSell && position != nil {
			var trade domain.Trade
			cash, trade = closePosition(cash, position, candle.Time, candle.Open*(1-slippageRate), feeRate, pending.Reason)
			trades = append(trades, trade)
			position = nil
		}
		pending = domain.Signal{Action: domain.SignalHold}

		if position != nil && candle.Low <= position.StopPrice {
			stopFill := position.StopPrice * (1 - slippageRate)
			if candle.Open < position.StopPrice {
				stopFill = candle.Open * (1 - slippageRate)
			}
			var trade domain.Trade
			cash, trade = closePosition(cash, position, candle.Time, stopFill, feeRate, "защитный ATR-стоп")
			trades = append(trades, trade)
			position = nil
		}

		equity := cash
		if position != nil {
			equity += position.Quantity * candle.Close
			exposedBars++
		}
		e.risk.UpdateEquity(equity)
		curve = append(curve, domain.EquityPoint{Time: candle.Time, Equity: equity})

		signal := e.strategy.Evaluate(candles[:i+1], position != nil)
		if position != nil && signal.ATR > 0 {
			position.StopPrice = math.Max(position.StopPrice, e.risk.TrailingStop(candle.Close, signal.ATR))
		}
		if signal.Action == domain.SignalBuy && position == nil && !e.risk.Halted(equity) {
			pending = signal
		} else if signal.Action == domain.SignalSell && position != nil {
			pending = signal
		}
	}

	if position != nil {
		last := candles[len(candles)-1]
		var trade domain.Trade
		cash, trade = closePosition(cash, position, last.Time, last.Close*(1-slippageRate), feeRate, "конец тестового периода")
		trades = append(trades, trade)
		curve[len(curve)-1].Equity = cash
	}

	return domain.BacktestResult{
		Statistics:  calculateStatistics(e.config.InitialCapital, cash, candles, curve, trades, exposedBars),
		Trades:      trades,
		EquityCurve: curve,
	}, nil
}

func closePosition(cash float64, position *domain.Position, exitTime time.Time, exitPrice, feeRate float64, reason string) (float64, domain.Trade) {
	exitFee := exitPrice * position.Quantity * feeRate
	cash += exitPrice*position.Quantity - exitFee
	pnl := (exitPrice-position.EntryPrice)*position.Quantity - position.EntryFee - exitFee
	return cash, domain.Trade{
		EntryTime: position.EntryTime, ExitTime: exitTime, EntryPrice: position.EntryPrice,
		ExitPrice: exitPrice, Quantity: position.Quantity, Fees: position.EntryFee + exitFee,
		PnL: pnl, PnLPercent: pnl / (position.EntryPrice * position.Quantity), ExitReason: reason,
	}
}

func calculateStatistics(initial, final float64, candles []domain.Candle, curve []domain.EquityPoint, trades []domain.Trade, exposedBars int) domain.Statistics {
	peak := curve[0].Equity
	maxDrawdown := 0.0
	returns := make([]float64, 0, len(curve)-1)
	for i, point := range curve {
		peak = math.Max(peak, point.Equity)
		if peak > 0 {
			maxDrawdown = math.Max(maxDrawdown, (peak-point.Equity)/peak)
		}
		if i > 0 && curve[i-1].Equity > 0 {
			returns = append(returns, point.Equity/curve[i-1].Equity-1)
		}
	}

	profitable := 0
	grossProfit := 0.0
	grossLoss := 0.0
	for _, trade := range trades {
		if trade.PnL > 0 {
			profitable++
			grossProfit += trade.PnL
		} else if trade.PnL < 0 {
			grossLoss += -trade.PnL
		}
	}
	profitFactor := 0.0
	if grossLoss > 0 {
		profitFactor = grossProfit / grossLoss
	}
	winRate := 0.0
	if len(trades) > 0 {
		winRate = float64(profitable) / float64(len(trades))
	}

	return domain.Statistics{
		InitialCapital: initial, FinalEquity: final, TotalReturn: final/initial - 1,
		BuyAndHold:  candles[len(candles)-1].Close/candles[0].Open - 1,
		MaxDrawdown: maxDrawdown, Sharpe: sharpe(returns), ProfitFactor: profitFactor,
		WinRate: winRate, Exposure: float64(exposedBars) / float64(len(candles)),
		Trades: len(trades), ProfitableTrades: profitable,
	}
}

func sharpe(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	var mean float64
	for _, value := range returns {
		mean += value
	}
	mean /= float64(len(returns))
	var variance float64
	for _, value := range returns {
		variance += math.Pow(value-mean, 2)
	}
	variance /= float64(len(returns) - 1)
	if variance == 0 {
		return 0
	}
	return mean / math.Sqrt(variance) * math.Sqrt(252)
}
