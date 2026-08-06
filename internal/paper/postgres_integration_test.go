package paper_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/database"
	"github.com/PCenjoyer/TradingMaster/internal/paper"
	"github.com/PCenjoyer/TradingMaster/internal/safety"
)

func TestPostgresSafetyAndPaperJournal(t *testing.T) {
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
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		truncate table
			tradingmaster_paper_order_events, tradingmaster_paper_executions,
			tradingmaster_paper_orders, tradingmaster_paper_positions,
			tradingmaster_paper_market_prices, tradingmaster_paper_accounts,
			tradingmaster_safety_state
		restart identity cascade`)
	if err != nil {
		t.Fatal(err)
	}

	controller, err := safety.NewPostgresController(ctx, pool, safety.Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := paper.NewPostgresBroker(ctx, pool, controller, paper.Config{
		InitialCapital: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- controller.WithExecutionLock(ctx, false, func(context.Context) error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	activationDone := make(chan error, 1)
	go func() {
		_, _, activationErr := controller.Activate(ctx, "проверка блокировки", time.Now().UTC())
		activationDone <- activationErr
	}()
	select {
	case err := <-activationDone:
		t.Fatalf("durable kill switch обошёл транзакционную блокировку: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-activationDone; err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.Deactivate(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	buy, err := broker.Submit(ctx, paper.SubmitRequest{
		Symbol: "BTCUSDT", Side: paper.SideBuy, Quantity: 100, MarketPrice: 100,
	})
	if err != nil || buy.Status != paper.StatusFilled || buy.Execution == nil {
		t.Fatalf("покупка не исполнена: %#v, %v", buy, err)
	}
	portfolio, err := broker.Portfolio(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if portfolio.Cash != 0 || portfolio.Equity != 10_000 || len(portfolio.Positions) != 1 {
		t.Fatalf("неожиданный paper-портфель: %#v", portfolio)
	}

	if _, err := broker.Mark(ctx, paper.MarkRequest{
		Symbol: "BTCUSDT", Price: 96, ObservedAt: time.Now().UTC().Add(2 * time.Minute),
	}); err == nil {
		t.Fatal("будущая paper-цена должна быть отклонена")
	}
	mark, err := broker.Mark(ctx, paper.MarkRequest{
		Symbol: "BTCUSDT", Price: 96, ObservedAt: time.Now().UTC().Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !mark.SafetyState.Active || !mark.SafetyState.DailyLimitActive || mark.Portfolio.Equity != 9_600 {
		t.Fatalf("переоценка не активировала дневной лимит: %#v", mark)
	}
	rejected, err := broker.Submit(ctx, paper.SubmitRequest{
		Symbol: "ETHUSDT", Side: paper.SideBuy, Quantity: 1, MarketPrice: 50,
	})
	if err != nil || rejected.Status != paper.StatusRejected || !strings.Contains(rejected.RejectReason, "заблокировано") {
		t.Fatalf("защита не отклонила открывающую заявку: %#v, %v", rejected, err)
	}
	sell, err := broker.Submit(ctx, paper.SubmitRequest{
		Symbol: "BTCUSDT", Side: paper.SideSell, Quantity: 1, MarketPrice: 110,
	})
	if err != nil || sell.Status != paper.StatusFilled {
		t.Fatalf("закрывающая заявка заблокирована: %#v, %v", sell, err)
	}

	orders, err := broker.Orders(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 3 {
		t.Fatalf("в журнале ожидалось 3 заявки, получено %d", len(orders))
	}
	for _, order := range orders {
		if len(order.Events) != 2 {
			t.Fatalf("для заявки %s ожидалось 2 события, получено %d", order.ID, len(order.Events))
		}
	}
	if _, err := pool.Exec(ctx,
		"update tradingmaster_paper_executions set price = price + 1 where id = $1", buy.Execution.ID,
	); err == nil || !strings.Contains(err.Error(), "только для добавления") {
		t.Fatalf("append-only защита журнала не сработала: %v", err)
	}

	restored, err := safety.NewPostgresController(ctx, pool, safety.Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	state, err := restored.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Active || !state.DurableStorage || !strings.Contains(state.Reason, "дневной лимит") {
		t.Fatalf("durable safety-state не восстановлен: %#v", state)
	}
}
