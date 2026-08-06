package testexchange

import (
	"context"
	"errors"
	"time"
)

type Mode string

const (
	ModeValidate Mode = "validate"
	ModeExecute  Mode = "execute"

	StatusReserved  = "reserved"
	StatusValidated = "validated"
	StatusSubmitted = "submitted"
	StatusUnknown   = "unknown"
	StatusRejected  = "rejected"
)

var (
	ErrInvalidRequest      = errors.New("некорректный запрос тестовой биржи")
	ErrIdempotencyConflict = errors.New("ключ идемпотентности уже использован для другого запроса")
	ErrOrderNotFound       = errors.New("заявка тестовой биржи не найдена")
)

type Config struct {
	APIKey        string
	SecretKey     string
	Mode          Mode
	ReceiveWindow time.Duration
}

type ServiceStatus struct {
	Connected   bool      `json:"connected"`
	Exchange    string    `json:"exchange"`
	Environment string    `json:"environment"`
	Mode        Mode      `json:"order_mode"`
	ServerTime  time.Time `json:"server_time"`
}

type Balance struct {
	Asset  string `json:"asset"`
	Free   string `json:"free"`
	Locked string `json:"locked"`
}

type Account struct {
	CanTrade    bool      `json:"can_trade"`
	CanDeposit  bool      `json:"can_deposit"`
	CanWithdraw bool      `json:"can_withdraw"`
	UpdatedAt   time.Time `json:"updated_at"`
	Balances    []Balance `json:"balances"`
}

type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

type SubmitRequest struct {
	IdempotencyKey string    `json:"idempotency_key"`
	Symbol         string    `json:"symbol"`
	Side           Side      `json:"side"`
	OrderType      OrderType `json:"order_type"`
	Quantity       string    `json:"quantity"`
	Price          string    `json:"price,omitempty"`
}

type Order struct {
	ID                 int64        `json:"id"`
	IdempotencyKey     string       `json:"idempotency_key"`
	ClientOrderID      string       `json:"client_order_id"`
	ExchangeOrderID    *int64       `json:"exchange_order_id,omitempty"`
	Symbol             string       `json:"symbol"`
	Side               Side         `json:"side"`
	OrderType          OrderType    `json:"order_type"`
	Quantity           string       `json:"quantity"`
	Price              string       `json:"price,omitempty"`
	Mode               Mode         `json:"mode"`
	Status             string       `json:"status"`
	ExchangeStatus     string       `json:"exchange_status,omitempty"`
	ExecutedQuantity   string       `json:"executed_quantity,omitempty"`
	CumulativeQuoteQty string       `json:"cumulative_quote_quantity,omitempty"`
	ErrorCode          *int         `json:"error_code,omitempty"`
	ErrorMessage       string       `json:"error_message,omitempty"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
	Replayed           bool         `json:"replayed,omitempty"`
	Events             []OrderEvent `json:"events,omitempty"`
}

type OrderEvent struct {
	ID         int64     `json:"id"`
	OrderID    int64     `json:"order_id"`
	EventType  string    `json:"event_type"`
	Message    string    `json:"message,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type Broker interface {
	Enabled() bool
	Mode() Mode
	Status(context.Context) (ServiceStatus, error)
	Account(context.Context) (Account, error)
	Submit(context.Context, SubmitRequest) (Order, error)
	Orders(context.Context, int) ([]Order, error)
	Reconcile(context.Context, string) (Order, error)
}
