package testexchange

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/database"
	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/jackc/pgx/v5/pgxpool"
)

const integrationTestLockID int64 = 793046118305114

func TestPostgresTestnetJournalSafetyAndIdempotency(t *testing.T) {
	databaseURL := os.Getenv("TM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TM_TEST_DATABASE_URL не задан")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	releaseLock := acquireIntegrationTestLock(t, ctx, pool)
	defer releaseLock()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		truncate table
			tradingmaster_testnet_order_events, tradingmaster_testnet_orders,
			tradingmaster_safety_state
		restart identity cascade`)
	if err != nil {
		t.Fatal(err)
	}

	var submitCalls atomic.Int32
	var queryCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v3/time":
			_, _ = writer.Write([]byte(`{"serverTime":1754500000000}`))
		case "/api/v3/order":
			if request.Method == http.MethodPost {
				call := submitCalls.Add(1)
				if call == 1 {
					writer.WriteHeader(http.StatusInternalServerError)
					_, _ = writer.Write([]byte(`{"code":-1007,"msg":"Timeout waiting for response"}`))
					return
				}
				_, _ = writer.Write([]byte(`{
					"symbol":"BTCUSDT","orderId":102,"clientOrderId":"sell-order",
					"status":"FILLED","executedQty":"0.001","cummulativeQuoteQty":"60.0"
				}`))
				return
			}
			queryCalls.Add(1)
			_, _ = writer.Write([]byte(`{
				"symbol":"BTCUSDT","orderId":101,"clientOrderId":"buy-order",
				"status":"NEW","executedQty":"0.000","cummulativeQuoteQty":"0.0"
			}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	controller, err := safety.NewPostgresController(ctx, pool, safety.Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	client, err := newBinanceClientAt(Config{
		APIKey: "api", SecretKey: "secret", Mode: ModeExecute,
	}, server.URL, true, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	broker, err := newBinanceSpotBrokerWithClient(pool, controller, ModeExecute, client)
	if err != nil {
		t.Fatal(err)
	}

	request := SubmitRequest{
		IdempotencyKey: "strategy:20260806:0001", Symbol: "BTCUSDT",
		Side: SideBuy, OrderType: OrderTypeMarket, Quantity: "0.001",
	}
	order, err := broker.Submit(ctx, request)
	if err != nil || order.Status != StatusSubmitted || order.ExchangeOrderID == nil || *order.ExchangeOrderID != 101 {
		t.Fatalf("неоднозначная заявка не сверена: %#v, %v", order, err)
	}
	if submitCalls.Load() != 1 || queryCalls.Load() != 1 {
		t.Fatalf("неожиданное число обращений: submit=%d query=%d", submitCalls.Load(), queryCalls.Load())
	}
	replayed, err := broker.Submit(ctx, request)
	if err != nil || !replayed.Replayed || submitCalls.Load() != 1 {
		t.Fatalf("идемпотентный повтор отправил новую заявку: %#v, %v", replayed, err)
	}
	changed := request
	changed.Quantity = "0.002"
	if _, err := broker.Submit(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("конфликт ключа идемпотентности не обнаружен: %v", err)
	}
	if _, _, err := controller.Activate(ctx, "проверка тестовой биржи", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rejected, err := broker.Submit(ctx, SubmitRequest{
		IdempotencyKey: "strategy:20260806:0002", Symbol: "ETHUSDT",
		Side: SideBuy, OrderType: OrderTypeMarket, Quantity: "0.01",
	})
	if err != nil || rejected.Status != StatusRejected || !strings.Contains(rejected.ErrorMessage, "заблокировано") {
		t.Fatalf("kill switch не отклонил открывающую заявку: %#v, %v", rejected, err)
	}
	sell, err := broker.Submit(ctx, SubmitRequest{
		IdempotencyKey: "strategy:20260806:0003", Symbol: "BTCUSDT",
		Side: SideSell, OrderType: OrderTypeMarket, Quantity: "0.001",
	})
	if err != nil || sell.Status != StatusSubmitted || submitCalls.Load() != 2 {
		t.Fatalf("закрывающая заявка заблокирована: %#v, %v", sell, err)
	}
	orders, err := broker.Orders(ctx, 10)
	if err != nil || len(orders) != 3 {
		t.Fatalf("неожиданный durable-журнал: %#v, %v", orders, err)
	}
	if _, err := pool.Exec(ctx,
		"update tradingmaster_testnet_order_events set message = 'изменено' where order_id = $1", order.ID,
	); err == nil || !strings.Contains(err.Error(), "только для добавления") {
		t.Fatalf("append-only защита журнала не сработала: %v", err)
	}
}

func acquireIntegrationTestLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool) func() {
	t.Helper()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, "select pg_advisory_lock($1)", integrationTestLockID); err != nil {
		connection.Release()
		t.Fatal(err)
	}
	return func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := connection.Exec(unlockContext, "select pg_advisory_unlock($1)", integrationTestLockID); err != nil {
			t.Errorf("не удалось снять блокировку интеграционного теста: %v", err)
		}
		connection.Release()
	}
}
