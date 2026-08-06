package testexchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,63}$`)
	symbolPattern      = regexp.MustCompile(`^[A-Z0-9]{2,20}$`)
	decimalPattern     = regexp.MustCompile(`^(?:0\.[0-9]*[1-9][0-9]*|[1-9][0-9]*(?:\.[0-9]+)?)$`)
)

type BinanceSpotBroker struct {
	pool   *pgxpool.Pool
	safety *safety.Controller
	client *binanceClient
	mode   Mode
}

func NewBinanceSpotBroker(
	_ context.Context,
	pool *pgxpool.Pool,
	controller *safety.Controller,
	config Config,
) (*BinanceSpotBroker, error) {
	client, err := newBinanceClient(config)
	if err != nil {
		return nil, err
	}
	return newBinanceSpotBrokerWithClient(pool, controller, config.Mode, client)
}

func newBinanceSpotBrokerWithClient(
	pool *pgxpool.Pool,
	controller *safety.Controller,
	mode Mode,
	client *binanceClient,
) (*BinanceSpotBroker, error) {
	if pool == nil || controller == nil || client == nil {
		return nil, fmt.Errorf("PostgreSQL, safety controller и Binance client обязательны")
	}
	if !controller.Durable() {
		return nil, fmt.Errorf("адаптер тестовой биржи требует durable safety controller")
	}
	if mode == "" {
		mode = ModeValidate
	}
	if mode != ModeValidate && mode != ModeExecute {
		return nil, fmt.Errorf("режим тестовой биржи должен быть validate или execute")
	}
	return &BinanceSpotBroker{pool: pool, safety: controller, client: client, mode: mode}, nil
}

func (*BinanceSpotBroker) Enabled() bool { return true }
func (b *BinanceSpotBroker) Mode() Mode  { return b.mode }

func (b *BinanceSpotBroker) Status(ctx context.Context) (ServiceStatus, error) {
	return b.client.status(ctx, b.mode)
}

func (b *BinanceSpotBroker) Account(ctx context.Context) (Account, error) {
	return b.client.account(ctx)
}

func (b *BinanceSpotBroker) Submit(ctx context.Context, request SubmitRequest) (Order, error) {
	normalized, err := normalizeSubmitRequest(request)
	if err != nil {
		return Order{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	fingerprint := requestFingerprint(normalized, b.mode)
	clientOrderID := deterministicClientOrderID(normalized.IdempotencyKey)

	existing, err := b.orderByIdempotency(ctx, b.pool, normalized.IdempotencyKey, false)
	switch {
	case err == nil:
		if existingFingerprint(existing) != fingerprint {
			return Order{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		existing.Events, err = b.events(ctx, b.pool, existing.ID)
		return existing, err
	case !errors.Is(err, pgx.ErrNoRows):
		return Order{}, fmt.Errorf("проверить ключ идемпотентности: %w", err)
	}

	var order Order
	err = b.safety.WithExecutionLock(ctx, normalized.Side == SideBuy, func(operationContext context.Context) error {
		transaction, ok := safety.PostgresTransaction(operationContext)
		if !ok {
			return fmt.Errorf("заявка тестовой биржи выполняется вне safety-транзакции")
		}
		stored, findErr := b.orderByIdempotency(
			operationContext, transaction, normalized.IdempotencyKey, true,
		)
		if findErr == nil {
			if existingFingerprint(stored) != fingerprint {
				return ErrIdempotencyConflict
			}
			stored.Replayed = true
			order = stored
			return nil
		}
		if !errors.Is(findErr, pgx.ErrNoRows) {
			return findErr
		}
		operationTime, timeErr := databaseTime(operationContext, transaction)
		if timeErr != nil {
			return timeErr
		}
		order = Order{
			IdempotencyKey: normalized.IdempotencyKey, ClientOrderID: clientOrderID,
			Symbol: normalized.Symbol, Side: normalized.Side, OrderType: normalized.OrderType,
			Quantity: normalized.Quantity, Price: normalized.Price, Mode: b.mode,
			Status: StatusReserved, CreatedAt: operationTime, UpdatedAt: operationTime,
		}
		if _, err := insertOrder(operationContext, transaction, order, fingerprint, false); err != nil {
			return err
		}
		if err := transaction.QueryRow(operationContext,
			"select id from tradingmaster_testnet_orders where idempotency_key = $1",
			order.IdempotencyKey,
		).Scan(&order.ID); err != nil {
			return err
		}
		if err := insertEvent(operationContext, transaction, order.ID, StatusReserved, "", operationTime); err != nil {
			return err
		}

		remote, submitErr := b.client.submit(operationContext, normalized, clientOrderID, b.mode)
		if submitErr != nil && b.mode == ModeExecute && shouldQueryAfterSubmit(submitErr) {
			if queried, queryErr := b.client.queryOrder(operationContext, normalized.Symbol, clientOrderID); queryErr == nil {
				remote = queried
				submitErr = nil
			}
		}
		if submitErr == nil {
			status := StatusValidated
			eventMessage := "подпись и параметры приняты тестовой биржей без отправки в matching engine"
			if b.mode == ModeExecute {
				status = StatusSubmitted
				eventMessage = "заявка принята matching engine тестовой биржи"
			}
			applyRemoteOrder(&order, remote)
			order.Status = status
			order.UpdatedAt = operationTime
			if err := updateOrder(operationContext, transaction, order); err != nil {
				return err
			}
			return insertEvent(operationContext, transaction, order.ID, status, eventMessage, operationTime)
		}

		status := StatusRejected
		message := submitErr.Error()
		var code *int
		if apiErr, ok := extractAPIError(submitErr); ok {
			if apiErr.Ambiguous() {
				status = StatusUnknown
			}
			if apiErr.Code != 0 {
				value := apiErr.Code
				code = &value
			}
		}
		order.Status = status
		order.ErrorCode = code
		order.ErrorMessage = message
		order.UpdatedAt = operationTime
		if err := updateOrder(operationContext, transaction, order); err != nil {
			return err
		}
		return insertEvent(operationContext, transaction, order.ID, status, message, operationTime)
	})
	if errors.Is(err, safety.ErrTradingBlocked) {
		return b.persistSafetyRejected(ctx, normalized, fingerprint, clientOrderID, err.Error())
	}
	if err != nil {
		return Order{}, err
	}
	order.Events, err = b.events(ctx, b.pool, order.ID)
	return order, err
}

func (b *BinanceSpotBroker) persistSafetyRejected(
	ctx context.Context,
	request SubmitRequest,
	fingerprint, clientOrderID, reason string,
) (Order, error) {
	transaction, err := b.pool.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	existing, err := b.orderByIdempotency(ctx, transaction, request.IdempotencyKey, true)
	if err == nil {
		if existingFingerprint(existing) != fingerprint {
			return Order{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		if err := transaction.Commit(ctx); err != nil {
			return Order{}, err
		}
		existing.Events, err = b.events(ctx, b.pool, existing.ID)
		return existing, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Order{}, err
	}
	now, err := databaseTime(ctx, transaction)
	if err != nil {
		return Order{}, err
	}
	order := Order{
		IdempotencyKey: request.IdempotencyKey, ClientOrderID: clientOrderID,
		Symbol: request.Symbol, Side: request.Side, OrderType: request.OrderType,
		Quantity: request.Quantity, Price: request.Price, Mode: b.mode,
		Status: StatusRejected, ErrorMessage: reason, CreatedAt: now, UpdatedAt: now,
	}
	inserted, err := insertOrder(ctx, transaction, order, fingerprint, true)
	if err != nil {
		return Order{}, err
	}
	if !inserted {
		existing, err := b.orderByIdempotency(ctx, transaction, request.IdempotencyKey, true)
		if err != nil {
			return Order{}, err
		}
		if existingFingerprint(existing) != fingerprint {
			return Order{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		if err := transaction.Commit(ctx); err != nil {
			return Order{}, err
		}
		existing.Events, err = b.events(ctx, b.pool, existing.ID)
		return existing, err
	}
	if err := transaction.QueryRow(ctx,
		"select id from tradingmaster_testnet_orders where idempotency_key = $1",
		order.IdempotencyKey,
	).Scan(&order.ID); err != nil {
		return Order{}, err
	}
	if err := insertEvent(ctx, transaction, order.ID, StatusRejected, reason, now); err != nil {
		return Order{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Order{}, err
	}
	order.Events, err = b.events(ctx, b.pool, order.ID)
	return order, err
}

func (b *BinanceSpotBroker) Orders(ctx context.Context, limit int) ([]Order, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := b.pool.Query(ctx, selectOrders+" order by created_at desc, id desc limit $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := make([]Order, 0, limit)
	for rows.Next() {
		order, _, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range orders {
		orders[index].Events, err = b.events(ctx, b.pool, orders[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return orders, nil
}

func (b *BinanceSpotBroker) Reconcile(ctx context.Context, idempotencyKey string) (Order, error) {
	if !idempotencyPattern.MatchString(idempotencyKey) {
		return Order{}, fmt.Errorf("%w: некорректный ключ идемпотентности", ErrInvalidRequest)
	}
	transaction, err := b.pool.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	order, err := b.orderByIdempotency(ctx, transaction, idempotencyKey, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrOrderNotFound
	}
	if err != nil {
		return Order{}, err
	}
	if order.Mode != ModeExecute || order.Status == StatusValidated || order.Status == StatusRejected {
		order.Replayed = true
		if err := transaction.Commit(ctx); err != nil {
			return Order{}, err
		}
		order.Events, err = b.events(ctx, b.pool, order.ID)
		return order, err
	}
	remote, queryErr := b.client.queryOrder(ctx, order.Symbol, order.ClientOrderID)
	if queryErr != nil {
		if apiErr, ok := extractAPIError(queryErr); ok && apiErr.Code == -2013 {
			if err := transaction.Commit(ctx); err != nil {
				return Order{}, err
			}
			order.Events, err = b.events(ctx, b.pool, order.ID)
			return order, err
		}
		return Order{}, queryErr
	}
	now, err := databaseTime(ctx, transaction)
	if err != nil {
		return Order{}, err
	}
	applyRemoteOrder(&order, remote)
	order.Status = StatusSubmitted
	order.ErrorCode = nil
	order.ErrorMessage = ""
	order.UpdatedAt = now
	if err := updateOrder(ctx, transaction, order); err != nil {
		return Order{}, err
	}
	if err := insertEvent(ctx, transaction, order.ID, "reconciled", "состояние сверено с тестовой биржей", now); err != nil {
		return Order{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Order{}, err
	}
	order.Events, err = b.events(ctx, b.pool, order.ID)
	return order, err
}

func normalizeSubmitRequest(request SubmitRequest) (SubmitRequest, error) {
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.Symbol = strings.ToUpper(strings.TrimSpace(request.Symbol))
	request.Quantity = strings.TrimSpace(request.Quantity)
	request.Price = strings.TrimSpace(request.Price)
	request.Side = Side(strings.ToLower(strings.TrimSpace(string(request.Side))))
	request.OrderType = OrderType(strings.ToLower(strings.TrimSpace(string(request.OrderType))))
	if request.OrderType == "" {
		request.OrderType = OrderTypeMarket
	}
	if !idempotencyPattern.MatchString(request.IdempotencyKey) {
		return SubmitRequest{}, fmt.Errorf("idempotency_key должен содержать 8–64 безопасных символа")
	}
	if !symbolPattern.MatchString(request.Symbol) {
		return SubmitRequest{}, fmt.Errorf("некорректный биржевой символ")
	}
	if request.Side != SideBuy && request.Side != SideSell {
		return SubmitRequest{}, fmt.Errorf("side должен быть buy или sell")
	}
	if request.OrderType != OrderTypeMarket && request.OrderType != OrderTypeLimit {
		return SubmitRequest{}, fmt.Errorf("order_type должен быть market или limit")
	}
	if len(request.Quantity) > 32 || !decimalPattern.MatchString(request.Quantity) {
		return SubmitRequest{}, fmt.Errorf("quantity должна быть положительной десятичной строкой")
	}
	if request.OrderType == OrderTypeMarket && request.Price != "" {
		return SubmitRequest{}, fmt.Errorf("price нельзя передавать для market-заявки")
	}
	if request.OrderType == OrderTypeLimit && (len(request.Price) > 32 || !decimalPattern.MatchString(request.Price)) {
		return SubmitRequest{}, fmt.Errorf("для limit-заявки нужна положительная десятичная price")
	}
	return request, nil
}

func requestFingerprint(request SubmitRequest, mode Mode) string {
	payload := strings.Join([]string{
		request.Symbol, string(request.Side), string(request.OrderType), request.Quantity, request.Price, string(mode),
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func deterministicClientOrderID(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))
	return "tm-" + hex.EncodeToString(sum[:])[:28]
}

func shouldQueryAfterSubmit(err error) bool {
	apiErr, ok := extractAPIError(err)
	return ok && (apiErr.Ambiguous() || apiErr.Code == -2010)
}

func applyRemoteOrder(order *Order, remote remoteOrder) {
	if remote.OrderID != 0 {
		value := remote.OrderID
		order.ExchangeOrderID = &value
	}
	order.ExchangeStatus = remote.Status
	order.ExecutedQuantity = remote.ExecutedQuantity
	order.CumulativeQuoteQty = remote.CumulativeQuoteQty
}

func insertOrder(
	ctx context.Context,
	transaction pgx.Tx,
	order Order,
	fingerprint string,
	ignoreIdempotencyConflict bool,
) (bool, error) {
	query := `
		insert into tradingmaster_testnet_orders(
			idempotency_key, request_fingerprint, client_order_id, exchange_order_id,
			symbol, side, order_type, quantity, price, mode, status, exchange_status,
			executed_quantity, cumulative_quote_quantity, error_code, error_message,
			created_at, updated_at
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`
	if ignoreIdempotencyConflict {
		query += " on conflict (idempotency_key) do nothing"
	}
	command, err := transaction.Exec(ctx, query,
		order.IdempotencyKey, fingerprint, order.ClientOrderID, order.ExchangeOrderID,
		order.Symbol, order.Side, order.OrderType, order.Quantity, order.Price, order.Mode,
		order.Status, order.ExchangeStatus, order.ExecutedQuantity, order.CumulativeQuoteQty,
		order.ErrorCode, order.ErrorMessage, order.CreatedAt, order.UpdatedAt,
	)
	return command.RowsAffected() == 1, err
}

func updateOrder(ctx context.Context, transaction pgx.Tx, order Order) error {
	_, err := transaction.Exec(ctx, `
		update tradingmaster_testnet_orders set
			exchange_order_id = $1, status = $2, exchange_status = $3,
			executed_quantity = $4, cumulative_quote_quantity = $5,
			error_code = $6, error_message = $7, updated_at = $8
		where id = $9`,
		order.ExchangeOrderID, order.Status, order.ExchangeStatus, order.ExecutedQuantity,
		order.CumulativeQuoteQty, order.ErrorCode, order.ErrorMessage, order.UpdatedAt, order.ID,
	)
	return err
}

func insertEvent(
	ctx context.Context,
	transaction pgx.Tx,
	orderID int64,
	eventType, message string,
	when time.Time,
) error {
	_, err := transaction.Exec(ctx, `
		insert into tradingmaster_testnet_order_events(order_id, event_type, message, occurred_at)
		values ($1, $2, $3, $4)`, orderID, eventType, message, when)
	return err
}

const selectOrders = `
	select id, idempotency_key, request_fingerprint, client_order_id, exchange_order_id,
		symbol, side, order_type, quantity, price, mode, status, exchange_status,
		executed_quantity, cumulative_quote_quantity, error_code, error_message,
		created_at, updated_at
	from tradingmaster_testnet_orders`

type rowScanner interface {
	Scan(...any) error
}

type orderQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (b *BinanceSpotBroker) orderByIdempotency(
	ctx context.Context,
	querier orderQuerier,
	idempotencyKey string,
	forUpdate bool,
) (Order, error) {
	query := selectOrders + " where idempotency_key = $1"
	if forUpdate {
		query += " for update"
	}
	order, _, err := scanOrder(querier.QueryRow(ctx, query, idempotencyKey))
	return order, err
}

func scanOrder(scanner rowScanner) (Order, string, error) {
	var order Order
	var fingerprint string
	err := scanner.Scan(
		&order.ID, &order.IdempotencyKey, &fingerprint, &order.ClientOrderID, &order.ExchangeOrderID,
		&order.Symbol, &order.Side, &order.OrderType, &order.Quantity, &order.Price, &order.Mode,
		&order.Status, &order.ExchangeStatus, &order.ExecutedQuantity, &order.CumulativeQuoteQty,
		&order.ErrorCode, &order.ErrorMessage, &order.CreatedAt, &order.UpdatedAt,
	)
	order.ErrorMessage = strings.TrimSpace(order.ErrorMessage)
	return order, fingerprint, err
}

func existingFingerprint(order Order) string {
	return requestFingerprint(SubmitRequest{
		IdempotencyKey: order.IdempotencyKey, Symbol: order.Symbol, Side: order.Side,
		OrderType: order.OrderType, Quantity: order.Quantity, Price: order.Price,
	}, order.Mode)
}

func (b *BinanceSpotBroker) events(
	ctx context.Context,
	querier orderQuerier,
	orderID int64,
) ([]OrderEvent, error) {
	rows, err := querier.Query(ctx, `
		select id, order_id, event_type, message, occurred_at
		from tradingmaster_testnet_order_events where order_id = $1 order by id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]OrderEvent, 0, 3)
	for rows.Next() {
		var event OrderEvent
		if err := rows.Scan(&event.ID, &event.OrderID, &event.EventType, &event.Message, &event.OccurredAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func databaseTime(ctx context.Context, transaction pgx.Tx) (time.Time, error) {
	var now time.Time
	if err := transaction.QueryRow(ctx, "select clock_timestamp()").Scan(&now); err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
}
