package shadow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/PCenjoyer/TradingMaster/internal/strategy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const shadowProcessingLockID int64 = 8401742630183852

type engine struct {
	pool     *pgxpool.Pool
	strategy *strategy.TrendBreakout
	safety   *safety.Controller
}

func newEngine(pool *pgxpool.Pool, controller *safety.Controller, config strategy.Config) (*engine, error) {
	if pool == nil || controller == nil || !controller.Durable() {
		return nil, fmt.Errorf("shadow engine требует PostgreSQL и durable safety-store")
	}
	strategyEngine, err := strategy.NewTrendBreakout(config)
	if err != nil {
		return nil, err
	}
	return &engine{pool: pool, strategy: strategyEngine, safety: controller}, nil
}

func (engine *engine) latestCandleTime(ctx context.Context, symbol, interval string) (time.Time, error) {
	var value *time.Time
	if err := engine.pool.QueryRow(ctx, `
		select max(open_time) from tradingmaster_shadow_candles
		where symbol = $1 and interval = $2`, symbol, interval).Scan(&value); err != nil {
		return time.Time{}, err
	}
	if value == nil {
		return time.Time{}, nil
	}
	return value.UTC(), nil
}

func (engine *engine) process(
	ctx context.Context,
	candle MarketCandle,
	evaluate bool,
) (Signal, bool, error) {
	if err := candle.Candle.Validate(); err != nil {
		return Signal{}, false, err
	}
	transaction, err := engine.pool.Begin(ctx)
	if err != nil {
		return Signal{}, false, err
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	if _, err := transaction.Exec(ctx, "select pg_advisory_xact_lock($1)", shadowProcessingLockID); err != nil {
		return Signal{}, false, err
	}
	var latest *time.Time
	if err := transaction.QueryRow(ctx, `
		select max(open_time) from tradingmaster_shadow_candles
		where symbol = $1 and interval = $2`, candle.Symbol, candle.Interval).Scan(&latest); err != nil {
		return Signal{}, false, err
	}
	if latest != nil && !candle.Candle.Time.After(latest.UTC()) {
		return Signal{}, false, nil
	}
	if _, err := transaction.Exec(ctx, `
		insert into tradingmaster_shadow_candles(
			symbol, interval, open_time, close_time, open, high, low, close, volume
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		candle.Symbol, candle.Interval, candle.Candle.Time, candle.CloseTime,
		candle.Candle.Open, candle.Candle.High, candle.Candle.Low, candle.Candle.Close, candle.Candle.Volume,
	); err != nil {
		return Signal{}, false, err
	}
	if !evaluate {
		if err := transaction.Commit(ctx); err != nil {
			return Signal{}, false, err
		}
		return Signal{}, true, nil
	}

	candles, err := candlesInTransaction(ctx, transaction, candle.Symbol, candle.Interval, 1_000)
	if err != nil {
		return Signal{}, false, err
	}
	inPosition, err := positionInTransaction(ctx, transaction, candle.Symbol, candle.Interval)
	if err != nil {
		return Signal{}, false, err
	}
	result := engine.strategy.Evaluate(candles, inPosition)
	state, safetyErr := engine.safety.State(ctx)
	blocked := false
	blockReason := ""
	eligible := result.Action == domain.SignalBuy || result.Action == domain.SignalSell
	if result.Action == domain.SignalBuy {
		switch {
		case safetyErr != nil:
			blocked, blockReason, eligible = true, "safety-store недоступен", false
		case state.Active:
			blocked, blockReason, eligible = true, state.Reason, false
		}
	}
	signal := Signal{
		Symbol: candle.Symbol, Interval: candle.Interval, CandleTime: candle.Candle.Time,
		ClosePrice: candle.Candle.Close, Action: result.Action, Reason: result.Reason,
		ATR: result.ATR, InPositionBefore: inPosition, EligibleForOrder: eligible,
		Blocked: blocked, BlockReason: blockReason, OrderSent: false,
	}
	if err := transaction.QueryRow(ctx, `
		insert into tradingmaster_shadow_signals(
			symbol, interval, candle_time, close_price, action, reason, atr,
			in_position_before, eligible_for_order, blocked, block_reason, order_sent
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,false)
		returning id, created_at`,
		signal.Symbol, signal.Interval, signal.CandleTime, signal.ClosePrice,
		signal.Action, signal.Reason, signal.ATR, signal.InPositionBefore,
		signal.EligibleForOrder, signal.Blocked, signal.BlockReason,
	).Scan(&signal.ID, &signal.CreatedAt); err != nil {
		return Signal{}, false, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Signal{}, false, err
	}
	return signal, true, nil
}

func candlesInTransaction(
	ctx context.Context,
	transaction pgx.Tx,
	symbol, interval string,
	limit int,
) ([]domain.Candle, error) {
	rows, err := transaction.Query(ctx, `
		select open_time, open, high, low, close, volume from (
			select open_time, open, high, low, close, volume
			from tradingmaster_shadow_candles
			where symbol = $1 and interval = $2
			order by open_time desc limit $3
		) history order by open_time`, symbol, interval, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Candle, 0, limit)
	for rows.Next() {
		var candle domain.Candle
		if err := rows.Scan(&candle.Time, &candle.Open, &candle.High, &candle.Low, &candle.Close, &candle.Volume); err != nil {
			return nil, err
		}
		result = append(result, candle)
	}
	return result, rows.Err()
}

func positionInTransaction(ctx context.Context, transaction pgx.Tx, symbol, interval string) (bool, error) {
	var action domain.SignalAction
	err := transaction.QueryRow(ctx, `
		select action from tradingmaster_shadow_signals
		where symbol = $1 and interval = $2 and eligible_for_order
			and action in ('buy','sell')
		order by candle_time desc, id desc limit 1`, symbol, interval).Scan(&action)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return action == domain.SignalBuy, err
}
