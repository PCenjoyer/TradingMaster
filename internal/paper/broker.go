package paper

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"

	StatusFilled   = "filled"
	StatusRejected = "rejected"
)

var ErrInvalidRequest = errors.New("некорректный paper-запрос")

var symbolPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,31}$`)

type Config struct {
	InitialCapital float64
	FeeBPS         float64
	SlippageBPS    float64
}

type SubmitRequest struct {
	Symbol      string  `json:"symbol"`
	Side        Side    `json:"side"`
	Quantity    float64 `json:"quantity"`
	MarketPrice float64 `json:"market_price"`
}

type Order struct {
	ID               string             `json:"id"`
	CreatedAt        time.Time          `json:"created_at"`
	Symbol           string             `json:"symbol"`
	Side             Side               `json:"side"`
	OrderType        string             `json:"order_type"`
	Quantity         float64            `json:"quantity"`
	MarketPrice      float64            `json:"market_price"`
	Status           string             `json:"status"`
	RejectReason     string             `json:"reject_reason,omitempty"`
	Execution        *Execution         `json:"execution,omitempty"`
	Events           []OrderEvent       `json:"events,omitempty"`
	SafetyState      *safety.State      `json:"safety_state,omitempty"`
	SafetyTransition *safety.Transition `json:"-"`
}

type OrderEvent struct {
	ID         int64     `json:"id"`
	OrderID    string    `json:"order_id"`
	EventType  string    `json:"event_type"`
	Reason     string    `json:"reason,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type Execution struct {
	ID         string    `json:"id"`
	OrderID    string    `json:"order_id"`
	Quantity   float64   `json:"quantity"`
	Price      float64   `json:"price"`
	Commission float64   `json:"commission"`
	ExecutedAt time.Time `json:"executed_at"`
}

type Position struct {
	Symbol        string  `json:"symbol"`
	Quantity      float64 `json:"quantity"`
	AveragePrice  float64 `json:"average_price"`
	CurrentPrice  float64 `json:"current_price"`
	MarketValue   float64 `json:"market_value"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
}

type Portfolio struct {
	Cash      float64    `json:"cash"`
	Equity    float64    `json:"equity"`
	Positions []Position `json:"positions"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type MarkRequest struct {
	Symbol     string    `json:"symbol"`
	Price      float64   `json:"price"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

type MarkResult struct {
	Portfolio   Portfolio         `json:"portfolio"`
	SafetyState safety.State      `json:"safety_state"`
	Transition  safety.Transition `json:"-"`
}

type Broker interface {
	Submit(context.Context, SubmitRequest) (Order, error)
	Mark(context.Context, MarkRequest) (MarkResult, error)
	Orders(context.Context, int) ([]Order, error)
	Portfolio(context.Context) (Portfolio, error)
	Enabled() bool
}

type PostgresBroker struct {
	pool     *pgxpool.Pool
	safety   *safety.Controller
	feeRate  float64
	slippage float64
	clock    func() time.Time
}

func NewPostgresBroker(ctx context.Context, pool *pgxpool.Pool, controller *safety.Controller, config Config) (*PostgresBroker, error) {
	if pool == nil || controller == nil {
		return nil, fmt.Errorf("PostgreSQL и safety controller обязательны")
	}
	if !controller.Durable() {
		return nil, fmt.Errorf("paper broker требует durable safety controller")
	}
	if config.InitialCapital <= 0 {
		return nil, fmt.Errorf("начальный paper-капитал должен быть положительным")
	}
	if config.FeeBPS < 0 || config.FeeBPS > 1000 || config.SlippageBPS < 0 || config.SlippageBPS > 1000 {
		return nil, fmt.Errorf("paper-комиссия и проскальзывание должны быть в диапазоне [0; 1000] bps")
	}
	now := time.Now().UTC()
	_, err := pool.Exec(ctx, `
		insert into tradingmaster_paper_accounts(id, cash, initial_capital, updated_at)
		values (1, $1, $1, $2) on conflict (id) do nothing`, config.InitialCapital, now)
	if err != nil {
		return nil, fmt.Errorf("инициализировать paper-счёт: %w", err)
	}
	broker := &PostgresBroker{
		pool: pool, safety: controller, feeRate: config.FeeBPS / 10_000,
		slippage: config.SlippageBPS / 10_000, clock: func() time.Time { return time.Now().UTC() },
	}
	portfolio, err := broker.Portfolio(ctx)
	if err != nil {
		return nil, fmt.Errorf("прочитать paper-счёт: %w", err)
	}
	if _, _, err := controller.ObserveEquity(ctx, time.Now().UTC(), portfolio.Equity); err != nil &&
		!errors.Is(err, safety.ErrStaleEquity) {
		return nil, fmt.Errorf("инициализировать paper equity: %w", err)
	}
	return broker, nil
}

func (*PostgresBroker) Enabled() bool { return true }

func (b *PostgresBroker) Submit(ctx context.Context, request SubmitRequest) (Order, error) {
	normalized, err := normalizeOrderRequest(request)
	if err != nil {
		return Order{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	orderID, err := newID()
	if err != nil {
		return Order{}, err
	}
	order := Order{
		ID: orderID, CreatedAt: b.clock(), Symbol: normalized.Symbol, Side: normalized.Side,
		OrderType: "market", Quantity: normalized.Quantity, MarketPrice: normalized.MarketPrice,
	}

	execute := func(operationContext context.Context) error {
		var executionErr error
		order, executionErr = b.execute(operationContext, order)
		return executionErr
	}
	err = b.safety.WithExecutionLock(ctx, normalized.Side == SideBuy, execute)
	if errors.Is(err, safety.ErrTradingBlocked) {
		return b.persistRejected(ctx, order, err.Error())
	}
	if err != nil {
		return Order{}, fmt.Errorf("исполнить paper-заявку: %w", err)
	}
	return order, nil
}

func (b *PostgresBroker) execute(ctx context.Context, order Order) (Order, error) {
	transaction, ok := safety.PostgresTransaction(ctx)
	if !ok {
		return Order{}, fmt.Errorf("paper-заявка выполняется вне safety-транзакции")
	}
	return b.executeInTransaction(ctx, transaction, order)
}

func (b *PostgresBroker) executeInTransaction(ctx context.Context, transaction pgx.Tx, order Order) (Order, error) {
	operationTime, err := databaseTime(ctx, transaction)
	if err != nil {
		return Order{}, err
	}
	order.CreatedAt = operationTime
	if err := createOrder(ctx, transaction, order); err != nil {
		return Order{}, err
	}
	var cash float64
	if err := transaction.QueryRow(ctx,
		"select cash from tradingmaster_paper_accounts where id = 1 for update",
	).Scan(&cash); err != nil {
		return Order{}, err
	}

	fillPrice := order.MarketPrice
	if order.Side == SideBuy {
		fillPrice *= 1 + b.slippage
	} else {
		fillPrice *= 1 - b.slippage
	}
	commission := fillPrice * order.Quantity * b.feeRate
	if order.Side == SideBuy {
		cost := fillPrice*order.Quantity + commission
		if cost > cash {
			return rejectInTransaction(ctx, transaction, order, "недостаточно денежных средств", operationTime)
		}
		if err := b.applyBuy(ctx, transaction, order, fillPrice, cost, cash, operationTime); err != nil {
			return Order{}, err
		}
	} else {
		positionQuantity, err := positionQuantityForUpdate(ctx, transaction, order.Symbol)
		if err != nil {
			return Order{}, err
		}
		if order.Quantity > positionQuantity {
			return rejectInTransaction(ctx, transaction, order, "недостаточно актива для продажи", operationTime)
		}
		if err := b.applySell(ctx, transaction, order, fillPrice, commission, cash, positionQuantity, operationTime); err != nil {
			return Order{}, err
		}
	}

	executionID, err := newID()
	if err != nil {
		return Order{}, err
	}
	execution := &Execution{
		ID: executionID, OrderID: order.ID, Quantity: order.Quantity, Price: fillPrice,
		Commission: commission, ExecutedAt: operationTime,
	}
	if _, err := transaction.Exec(ctx, `
		insert into tradingmaster_paper_executions(id, order_id, quantity, price, commission, executed_at)
		values ($1, $2, $3, $4, $5, $6)`,
		execution.ID, execution.OrderID, execution.Quantity, execution.Price, execution.Commission, execution.ExecutedAt,
	); err != nil {
		return Order{}, err
	}
	if _, err := transaction.Exec(ctx,
		"update tradingmaster_paper_orders set status = 'filled' where id = $1", order.ID,
	); err != nil {
		return Order{}, err
	}
	if _, err := transaction.Exec(ctx, `
		insert into tradingmaster_paper_order_events(order_id, event_type, occurred_at)
		values ($1, 'filled', $2)`, order.ID, execution.ExecutedAt,
	); err != nil {
		return Order{}, err
	}
	if _, err := transaction.Exec(ctx, `
		insert into tradingmaster_paper_market_prices(symbol, price, observed_at)
		values ($1, $2, $3)
		on conflict (symbol) do update set price = excluded.price, observed_at = excluded.observed_at
		where tradingmaster_paper_market_prices.observed_at <= excluded.observed_at`,
		order.Symbol, order.MarketPrice, execution.ExecutedAt,
	); err != nil {
		return Order{}, err
	}
	portfolio, err := portfolioInTransaction(ctx, transaction)
	if err != nil {
		return Order{}, err
	}
	state, transition, err := b.safety.ObserveEquity(ctx, execution.ExecutedAt, portfolio.Equity)
	if err != nil {
		return Order{}, err
	}
	order.Status = StatusFilled
	order.Execution = execution
	order.SafetyState = &state
	order.SafetyTransition = &transition
	return order, nil
}

func (b *PostgresBroker) applyBuy(
	ctx context.Context,
	transaction pgx.Tx,
	order Order,
	fillPrice, cost, cash float64,
	operationTime time.Time,
) error {
	var currentQuantity, averagePrice float64
	err := transaction.QueryRow(ctx, `
		select quantity, average_price from tradingmaster_paper_positions
		where symbol = $1 for update`, order.Symbol,
	).Scan(&currentQuantity, &averagePrice)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	newQuantity := currentQuantity + order.Quantity
	newAverage := fillPrice
	if currentQuantity > 0 {
		newAverage = (currentQuantity*averagePrice + order.Quantity*fillPrice) / newQuantity
	}
	if _, err := transaction.Exec(ctx, `
		insert into tradingmaster_paper_positions(symbol, quantity, average_price, updated_at)
		values ($1, $2, $3, $4)
		on conflict (symbol) do update set quantity = excluded.quantity,
			average_price = excluded.average_price, updated_at = excluded.updated_at`,
		order.Symbol, newQuantity, newAverage, operationTime,
	); err != nil {
		return err
	}
	_, err = transaction.Exec(ctx,
		"update tradingmaster_paper_accounts set cash = $1, updated_at = $2 where id = 1",
		cash-cost, operationTime,
	)
	return err
}

func (b *PostgresBroker) applySell(
	ctx context.Context,
	transaction pgx.Tx,
	order Order,
	fillPrice, commission, cash, positionQuantity float64,
	operationTime time.Time,
) error {
	remaining := positionQuantity - order.Quantity
	if math.Abs(remaining) < 1e-12 {
		if _, err := transaction.Exec(ctx,
			"delete from tradingmaster_paper_positions where symbol = $1", order.Symbol,
		); err != nil {
			return err
		}
	} else {
		if _, err := transaction.Exec(ctx, `
			update tradingmaster_paper_positions set quantity = $1, updated_at = $2 where symbol = $3`,
			remaining, operationTime, order.Symbol,
		); err != nil {
			return err
		}
	}
	_, err := transaction.Exec(ctx,
		"update tradingmaster_paper_accounts set cash = $1, updated_at = $2 where id = 1",
		cash+fillPrice*order.Quantity-commission, operationTime,
	)
	return err
}

func (b *PostgresBroker) persistRejected(ctx context.Context, order Order, reason string) (Order, error) {
	transaction, err := b.pool.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	operationTime, err := databaseTime(ctx, transaction)
	if err != nil {
		return Order{}, err
	}
	order.CreatedAt = operationTime
	if err := createOrder(ctx, transaction, order); err != nil {
		return Order{}, err
	}
	result, err := rejectInTransaction(ctx, transaction, order, reason, operationTime)
	if err != nil {
		return Order{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Order{}, err
	}
	return result, nil
}

func createOrder(ctx context.Context, transaction pgx.Tx, order Order) error {
	if _, err := transaction.Exec(ctx, `
		insert into tradingmaster_paper_orders(
			id, created_at, symbol, side, order_type, quantity, market_price, status
		) values ($1, $2, $3, $4, 'market', $5, $6, 'created')`,
		order.ID, order.CreatedAt, order.Symbol, order.Side, order.Quantity, order.MarketPrice,
	); err != nil {
		return err
	}
	_, err := transaction.Exec(ctx, `
		insert into tradingmaster_paper_order_events(order_id, event_type, occurred_at)
		values ($1, 'created', $2)`, order.ID, order.CreatedAt,
	)
	return err
}

func rejectInTransaction(
	ctx context.Context,
	transaction pgx.Tx,
	order Order,
	reason string,
	operationTime time.Time,
) (Order, error) {
	if _, err := transaction.Exec(ctx, `
		update tradingmaster_paper_orders set status = 'rejected', reject_reason = $1 where id = $2`,
		reason, order.ID,
	); err != nil {
		return Order{}, err
	}
	if _, err := transaction.Exec(ctx, `
		insert into tradingmaster_paper_order_events(order_id, event_type, reason, occurred_at)
		values ($1, 'rejected', $2, $3)`, order.ID, reason, operationTime,
	); err != nil {
		return Order{}, err
	}
	order.Status = StatusRejected
	order.RejectReason = reason
	return order, nil
}

func positionQuantityForUpdate(ctx context.Context, transaction pgx.Tx, symbol string) (float64, error) {
	var quantity float64
	err := transaction.QueryRow(ctx, `
		select quantity from tradingmaster_paper_positions where symbol = $1 for update`, symbol,
	).Scan(&quantity)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return quantity, err
}

func (b *PostgresBroker) Mark(ctx context.Context, request MarkRequest) (MarkResult, error) {
	symbol := strings.ToUpper(strings.TrimSpace(request.Symbol))
	if !symbolPattern.MatchString(symbol) {
		return MarkResult{}, fmt.Errorf("%w: некорректный символ инструмента", ErrInvalidRequest)
	}
	if request.Price <= 0 || math.IsNaN(request.Price) || math.IsInf(request.Price, 0) {
		return MarkResult{}, fmt.Errorf("%w: цена должна быть положительным конечным числом", ErrInvalidRequest)
	}
	if request.ObservedAt.IsZero() {
		request.ObservedAt = b.clock()
	}
	if request.ObservedAt.After(b.clock().Add(time.Minute)) {
		return MarkResult{}, fmt.Errorf("%w: observed_at не может быть более чем на минуту в будущем", ErrInvalidRequest)
	}
	var result MarkResult
	err := b.safety.WithExecutionLock(ctx, false, func(operationContext context.Context) error {
		transaction, ok := safety.PostgresTransaction(operationContext)
		if !ok {
			return fmt.Errorf("paper-переоценка выполняется вне safety-транзакции")
		}
		safetyTime, err := databaseTime(operationContext, transaction)
		if err != nil {
			return err
		}
		command, err := transaction.Exec(operationContext, `
		insert into tradingmaster_paper_market_prices(symbol, price, observed_at)
		values ($1, $2, $3)
		on conflict (symbol) do update set price = excluded.price, observed_at = excluded.observed_at
		where tradingmaster_paper_market_prices.observed_at <= excluded.observed_at`,
			symbol, request.Price, request.ObservedAt.UTC(),
		)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			return fmt.Errorf("%w: устаревшая рыночная цена отклонена", ErrInvalidRequest)
		}
		if _, err := transaction.Exec(operationContext, `
		update tradingmaster_paper_accounts
		set updated_at = greatest(updated_at, $1) where id = 1`, safetyTime,
		); err != nil {
			return err
		}
		portfolio, err := portfolioInTransaction(operationContext, transaction)
		if err != nil {
			return err
		}
		state, transition, err := b.safety.ObserveEquity(operationContext, safetyTime, portfolio.Equity)
		if err != nil {
			return err
		}
		result = MarkResult{Portfolio: portfolio, SafetyState: state, Transition: transition}
		return nil
	})
	if err != nil {
		return MarkResult{}, err
	}
	return result, nil
}

func (b *PostgresBroker) Orders(ctx context.Context, limit int) ([]Order, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := b.pool.Query(ctx, `
		select id, created_at, symbol, side, order_type, quantity, market_price, status, reject_reason
		from tradingmaster_paper_orders order by created_at desc, id desc limit $1`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := make([]Order, 0, limit)
	for rows.Next() {
		var order Order
		if err := rows.Scan(
			&order.ID, &order.CreatedAt, &order.Symbol, &order.Side, &order.OrderType,
			&order.Quantity, &order.MarketPrice, &order.Status, &order.RejectReason,
		); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range orders {
		execution, err := b.execution(ctx, orders[index].ID)
		if err != nil {
			return nil, err
		}
		orders[index].Execution = execution
		events, err := b.events(ctx, orders[index].ID)
		if err != nil {
			return nil, err
		}
		orders[index].Events = events
	}
	return orders, nil
}

func (b *PostgresBroker) events(ctx context.Context, orderID string) ([]OrderEvent, error) {
	rows, err := b.pool.Query(ctx, `
		select id, order_id, event_type, reason, occurred_at
		from tradingmaster_paper_order_events where order_id = $1 order by id`, orderID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]OrderEvent, 0, 2)
	for rows.Next() {
		var event OrderEvent
		if err := rows.Scan(&event.ID, &event.OrderID, &event.EventType, &event.Reason, &event.OccurredAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (b *PostgresBroker) execution(ctx context.Context, orderID string) (*Execution, error) {
	var execution Execution
	err := b.pool.QueryRow(ctx, `
		select id, order_id, quantity, price, commission, executed_at
		from tradingmaster_paper_executions where order_id = $1`, orderID,
	).Scan(
		&execution.ID, &execution.OrderID, &execution.Quantity, &execution.Price,
		&execution.Commission, &execution.ExecutedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &execution, err
}

func (b *PostgresBroker) Portfolio(ctx context.Context) (Portfolio, error) {
	transaction, err := b.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return Portfolio{}, err
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	portfolio, err := portfolioInTransaction(ctx, transaction)
	if err != nil {
		return Portfolio{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Portfolio{}, err
	}
	return portfolio, nil
}

func portfolioInTransaction(ctx context.Context, transaction pgx.Tx) (Portfolio, error) {
	var portfolio Portfolio
	if err := transaction.QueryRow(ctx, `
		select cash, updated_at from tradingmaster_paper_accounts where id = 1`,
	).Scan(&portfolio.Cash, &portfolio.UpdatedAt); err != nil {
		return Portfolio{}, err
	}
	rows, err := transaction.Query(ctx, `
		select p.symbol, p.quantity, p.average_price, coalesce(m.price, p.average_price)
		from tradingmaster_paper_positions p
		left join tradingmaster_paper_market_prices m on m.symbol = p.symbol
		order by p.symbol`)
	if err != nil {
		return Portfolio{}, err
	}
	defer rows.Close()
	portfolio.Positions = make([]Position, 0)
	portfolio.Equity = portfolio.Cash
	for rows.Next() {
		var position Position
		if err := rows.Scan(&position.Symbol, &position.Quantity, &position.AveragePrice, &position.CurrentPrice); err != nil {
			return Portfolio{}, err
		}
		position.MarketValue = position.Quantity * position.CurrentPrice
		position.UnrealizedPnL = position.Quantity * (position.CurrentPrice - position.AveragePrice)
		portfolio.Equity += position.MarketValue
		portfolio.Positions = append(portfolio.Positions, position)
	}
	if err := rows.Err(); err != nil {
		return Portfolio{}, err
	}
	rows.Close()
	return portfolio, nil
}

func normalizeOrderRequest(request SubmitRequest) (SubmitRequest, error) {
	request.Symbol = strings.ToUpper(strings.TrimSpace(request.Symbol))
	if !symbolPattern.MatchString(request.Symbol) {
		return SubmitRequest{}, fmt.Errorf("некорректный символ инструмента")
	}
	if request.Side != SideBuy && request.Side != SideSell {
		return SubmitRequest{}, fmt.Errorf("side должен быть buy или sell")
	}
	if request.Quantity <= 0 || math.IsNaN(request.Quantity) || math.IsInf(request.Quantity, 0) {
		return SubmitRequest{}, fmt.Errorf("quantity должно быть положительным конечным числом")
	}
	if request.MarketPrice <= 0 || math.IsNaN(request.MarketPrice) || math.IsInf(request.MarketPrice, 0) {
		return SubmitRequest{}, fmt.Errorf("market_price должно быть положительным конечным числом")
	}
	return request, nil
}

func newID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("создать идентификатор заявки: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func databaseTime(ctx context.Context, transaction pgx.Tx) (time.Time, error) {
	var value time.Time
	if err := transaction.QueryRow(ctx, "select clock_timestamp()").Scan(&value); err != nil {
		return time.Time{}, err
	}
	return value.UTC(), nil
}
