package safety

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresBackend struct {
	pool *pgxpool.Pool
}

type transactionContextKey struct{}

func PostgresTransaction(ctx context.Context) (pgx.Tx, bool) {
	transaction, ok := ctx.Value(transactionContextKey{}).(pgx.Tx)
	return transaction, ok
}

func NewPostgresController(ctx context.Context, pool *pgxpool.Pool, config Config) (*Controller, error) {
	initial, err := initialState(config)
	if err != nil {
		return nil, err
	}
	backend := &postgresBackend{pool: pool}
	_, err = pool.Exec(ctx, `
		insert into tradingmaster_safety_state (
			id, manual_active, manual_reason, daily_limit_active, trading_day,
			day_start_equity, current_equity, daily_loss_ratio, daily_loss_limit,
			updated_at, last_equity_at
		) values (1, $1, $2, false, '', 0, 0, 0, $3, $4, null)
		on conflict (id) do update set
			daily_loss_limit = excluded.daily_loss_limit,
			daily_limit_active = tradingmaster_safety_state.daily_limit_active
				or tradingmaster_safety_state.daily_loss_ratio >= excluded.daily_loss_limit`,
		initial.manualActive, initial.manualReason, initial.dailyLossLimit, nullableTime(initial.updatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("инициализировать durable safety-state: %w", err)
	}
	return &Controller{backend: backend}, nil
}

func (p *postgresBackend) Read(ctx context.Context) (storedState, error) {
	return scanStoredState(p.pool.QueryRow(ctx, selectSafetyState))
}

func (p *postgresBackend) Update(ctx context.Context, mutation func(*storedState) error) (storedState, error) {
	if transaction, ok := PostgresTransaction(ctx); ok {
		current, err := scanStoredState(transaction.QueryRow(ctx, selectSafetyState+" for update"))
		if err != nil {
			return storedState{}, err
		}
		if err := mutation(&current); err != nil {
			return storedState{}, err
		}
		if err := writeStoredState(ctx, transaction, current); err != nil {
			return storedState{}, err
		}
		return current, nil
	}
	transaction, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return storedState{}, err
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	current, err := scanStoredState(transaction.QueryRow(ctx, selectSafetyState+" for update"))
	if err != nil {
		return storedState{}, err
	}
	if err := mutation(&current); err != nil {
		return storedState{}, err
	}
	if err := writeStoredState(ctx, transaction, current); err != nil {
		return storedState{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return storedState{}, err
	}
	return current, nil
}

func (p *postgresBackend) WithLock(ctx context.Context, operation func(context.Context, storedState) error) error {
	transaction, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	current, err := scanStoredState(transaction.QueryRow(ctx, selectSafetyState+" for update"))
	if err != nil {
		return err
	}
	operationContext := context.WithValue(ctx, transactionContextKey{}, transaction)
	if err := operation(operationContext, current); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func (*postgresBackend) Durable() bool { return true }

const selectSafetyState = `
	select manual_active, manual_reason, daily_limit_active, trading_day,
		day_start_equity, current_equity, daily_loss_ratio, daily_loss_limit,
		updated_at, last_equity_at
	from tradingmaster_safety_state where id = 1`

type rowScanner interface {
	Scan(...any) error
}

func scanStoredState(row rowScanner) (storedState, error) {
	var (
		state        storedState
		updatedAt    *time.Time
		lastEquityAt *time.Time
	)
	err := row.Scan(
		&state.manualActive, &state.manualReason, &state.dailyLimitActive, &state.tradingDay,
		&state.dayStartEquity, &state.currentEquity, &state.dailyLossRatio, &state.dailyLossLimit,
		&updatedAt, &lastEquityAt,
	)
	if updatedAt != nil {
		state.updatedAt = updatedAt.UTC()
	}
	if lastEquityAt != nil {
		state.lastEquityAt = lastEquityAt.UTC()
	}
	return state, err
}

func writeStoredState(ctx context.Context, transaction pgx.Tx, state storedState) error {
	_, err := transaction.Exec(ctx, `
		update tradingmaster_safety_state set
			manual_active = $1, manual_reason = $2, daily_limit_active = $3,
			trading_day = $4, day_start_equity = $5, current_equity = $6,
			daily_loss_ratio = $7, daily_loss_limit = $8, updated_at = $9,
			last_equity_at = $10
		where id = 1`,
		state.manualActive, state.manualReason, state.dailyLimitActive, state.tradingDay,
		state.dayStartEquity, state.currentEquity, state.dailyLossRatio, state.dailyLossLimit,
		nullableTime(state.updatedAt), nullableTime(state.lastEquityAt),
	)
	return err
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
