package shadow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/jackc/pgx/v5/pgxpool"
)

const shadowLeaderLockID int64 = 8401742630183853

type PostgresService struct {
	pool       *pgxpool.Pool
	config     Config
	engine     *engine
	client     *marketClient
	logger     *slog.Logger
	instanceID string
	now        func() time.Time
}

func NewPostgresService(
	pool *pgxpool.Pool,
	controller *safety.Controller,
	logger *slog.Logger,
	config Config,
) (*PostgresService, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	shadowEngine, err := newEngine(pool, controller, normalized.Strategy)
	if err != nil {
		return nil, err
	}
	instanceID, err := newInstanceID()
	if err != nil {
		return nil, fmt.Errorf("создать shadow instance id: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PostgresService{
		pool: pool, config: normalized, engine: shadowEngine,
		client: newMarketClient(), logger: logger, instanceID: instanceID,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (*PostgresService) Enabled() bool { return true }

func (service *PostgresService) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		connection, leader, err := service.tryLeadership(ctx)
		if err != nil {
			service.logger.Error("не удалось выбрать лидера shadow-режима", "ошибка", err)
			if !waitContext(ctx, service.config.ReconnectMax) {
				return nil
			}
			continue
		}
		if !leader {
			if !waitContext(ctx, 5*time.Second) {
				return nil
			}
			continue
		}
		service.logger.Info("экземпляр стал лидером shadow-режима", "символы", service.config.Symbols, "интервал", service.config.Interval)
		service.runLeader(ctx)
		service.releaseLeadership(connection)
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (service *PostgresService) runLeader(ctx context.Context) {
	backoff := service.config.ReconnectMin
	for ctx.Err() == nil {
		if err := service.setRuntime(ctx, false, nil, nil, "", false, 0, 0); err != nil {
			service.logger.Error("не удалось записать состояние shadow-лидера", "ошибка", err)
		}
		if err := service.backfill(ctx); err != nil {
			service.logger.Error("не удалось заполнить историю shadow-свечей", "ошибка", err)
			_ = service.setRuntime(ctx, false, nil, nil, err.Error(), true, 0, 0)
			if !waitContext(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, service.config.ReconnectMax)
			continue
		}
		if err := service.setRuntime(ctx, true, nil, nil, "", false, 0, 0); err != nil {
			service.logger.Error("не удалось записать подключение shadow-потока", "ошибка", err)
		}
		lastHeartbeat := time.Time{}
		streamEstablished := false
		err := service.client.stream(ctx, service.config.Symbols, service.config.Interval,
			func(eventAt time.Time, candle *MarketCandle) error {
				if !streamEstablished {
					backoff = service.config.ReconnectMin
					streamEstablished = true
				}
				now := service.now()
				if lastHeartbeat.IsZero() || now.Sub(lastHeartbeat) >= service.config.HeartbeatRate {
					if err := service.setRuntime(ctx, true, &eventAt, nil, "", false, 0, 0); err != nil {
						return err
					}
					lastHeartbeat = now
				}
				if candle == nil {
					return nil
				}
				signal, inserted, err := service.engine.process(ctx, *candle, true)
				if err != nil || !inserted {
					return err
				}
				signalCount := int64(1)
				if signal.Action == "" {
					signalCount = 0
				}
				return service.setRuntime(ctx, true, &eventAt, &candle.Candle.Time, "", false, 1, signalCount)
			})
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = service.setRuntime(shutdownContext, false, nil, nil, "", false, 0, 0)
			cancel()
			return
		}
		service.logger.Warn("shadow WebSocket отключён, выполняется переподключение", "ошибка", err, "через", backoff)
		_ = service.setRuntime(ctx, false, nil, nil, err.Error(), true, 0, 0)
		if !waitContext(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, service.config.ReconnectMax)
	}
}

func (service *PostgresService) backfill(ctx context.Context) error {
	for _, symbol := range service.config.Symbols {
		latest, err := service.engine.latestCandleTime(ctx, symbol, service.config.Interval)
		if err != nil {
			return err
		}
		candles, err := service.client.backfill(ctx, symbol, service.config.Interval, service.config.HistoryLimit)
		if err != nil {
			return err
		}
		for index, candle := range candles {
			evaluate := !latest.IsZero() || index == len(candles)-1
			signal, inserted, err := service.engine.process(ctx, candle, evaluate)
			if err != nil {
				return err
			}
			if !inserted {
				continue
			}
			signalCount := int64(0)
			if signal.Action != "" {
				signalCount = 1
			}
			if err := service.setRuntime(ctx, false, nil, &candle.Candle.Time, "", false, 1, signalCount); err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *PostgresService) Status(ctx context.Context) (Status, error) {
	status := Status{
		Enabled: true, Mode: "shadow", MarketDataSource: "Binance Spot public market data",
		Symbols: append([]string(nil), service.config.Symbols...), Interval: service.config.Interval,
		OrdersSent: 0,
	}
	var leaderID string
	err := service.pool.QueryRow(ctx, `
		select leader_id, connected, last_event_at, last_candle_at, last_error,
			reconnects, candles_processed, signals_generated, updated_at
		from tradingmaster_shadow_runtime where id = 1`).Scan(
		&leaderID, &status.Connected, &status.LastEventAt, &status.LastCandleAt,
		&status.LastError, &status.Reconnects, &status.CandlesProcessed,
		&status.SignalsGenerated, &status.UpdatedAt,
	)
	if err != nil {
		return Status{}, err
	}
	status.Leader = leaderID == service.instanceID
	if status.UpdatedAt != nil {
		staleAfter := max(3*supportedIntervals[service.config.Interval], 30*time.Second)
		if service.now().Sub(*status.UpdatedAt) > staleAfter {
			status.Connected = false
			if status.LastError == "" {
				status.LastError = "heartbeat shadow-потока устарел"
			}
		}
	}
	return status, nil
}

func (service *PostgresService) Signals(ctx context.Context, limit int) ([]Signal, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := service.pool.Query(ctx, `
		select id, symbol, interval, candle_time, close_price, action, reason, atr,
			in_position_before, eligible_for_order, blocked, block_reason, order_sent, created_at
		from tradingmaster_shadow_signals order by candle_time desc, id desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Signal, 0, limit)
	for rows.Next() {
		var signal Signal
		if err := rows.Scan(
			&signal.ID, &signal.Symbol, &signal.Interval, &signal.CandleTime,
			&signal.ClosePrice, &signal.Action, &signal.Reason, &signal.ATR,
			&signal.InPositionBefore, &signal.EligibleForOrder, &signal.Blocked,
			&signal.BlockReason, &signal.OrderSent, &signal.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, signal)
	}
	return result, rows.Err()
}

func (service *PostgresService) Candles(ctx context.Context, symbol string, limit int) ([]MarketCandle, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if !shadowSymbolPattern.MatchString(symbol) {
		return nil, fmt.Errorf("некорректный shadow-символ")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1_000 {
		limit = 1_000
	}
	rows, err := service.pool.Query(ctx, `
		select symbol, interval, open_time, close_time, open, high, low, close, volume from (
			select symbol, interval, open_time, close_time, open, high, low, close, volume
			from tradingmaster_shadow_candles where symbol = $1 and interval = $2
			order by open_time desc limit $3
		) history order by open_time`, symbol, service.config.Interval, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]MarketCandle, 0, limit)
	for rows.Next() {
		var candle MarketCandle
		if err := rows.Scan(
			&candle.Symbol, &candle.Interval, &candle.Candle.Time, &candle.CloseTime,
			&candle.Candle.Open, &candle.Candle.High, &candle.Candle.Low,
			&candle.Candle.Close, &candle.Candle.Volume,
		); err != nil {
			return nil, err
		}
		result = append(result, candle)
	}
	return result, rows.Err()
}

func (service *PostgresService) tryLeadership(ctx context.Context) (*pgxpool.Conn, bool, error) {
	connection, err := service.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var leader bool
	if err := connection.QueryRow(ctx, "select pg_try_advisory_lock($1)", shadowLeaderLockID).Scan(&leader); err != nil {
		connection.Release()
		return nil, false, err
	}
	if !leader {
		connection.Release()
		return nil, false, nil
	}
	return connection, true, nil
}

func (service *PostgresService) releaseLeadership(connection *pgxpool.Conn) {
	if connection == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = connection.Exec(ctx, "select pg_advisory_unlock($1)", shadowLeaderLockID)
	connection.Release()
}

func (service *PostgresService) setRuntime(
	ctx context.Context,
	connected bool,
	lastEventAt, lastCandleAt *time.Time,
	lastError string,
	reconnect bool,
	candles, signals int64,
) error {
	_, err := service.pool.Exec(ctx, `
		update tradingmaster_shadow_runtime set
			leader_id = $1,
			connected = $2,
			last_event_at = coalesce($3, last_event_at),
			last_candle_at = coalesce($4, last_candle_at),
			last_error = $5,
			reconnects = reconnects + $6,
			candles_processed = candles_processed + $7,
			signals_generated = signals_generated + $8,
			updated_at = clock_timestamp()
		where id = 1`, service.instanceID, connected, lastEventAt, lastCandleAt,
		lastError, boolToInt(reconnect), candles, signals)
	return err
}

func newInstanceID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func boolToInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

var _ Service = (*PostgresService)(nil)
