package shadow

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/database"
	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/PCenjoyer/TradingMaster/internal/safety"
	"github.com/PCenjoyer/TradingMaster/internal/strategy"
	"github.com/jackc/pgx/v5/pgxpool"
)

const integrationTestLockID int64 = 793046118305114

func TestPostgresShadowJournalNeverSendsOrders(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `
		truncate table tradingmaster_shadow_signals, tradingmaster_shadow_candles,
			tradingmaster_shadow_runtime, tradingmaster_safety_state restart identity cascade;
		insert into tradingmaster_shadow_runtime(id) values (1)`); err != nil {
		t.Fatal(err)
	}

	controller, err := safety.NewPostgresController(ctx, pool, safety.Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	shadowEngine, err := newEngine(pool, controller, strategy.Config{
		EntryPeriod: 2, ExitPeriod: 2, ATRPeriod: 2, TrendPeriod: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	btc := breakoutCandles("BTCUSDT", time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC))
	for index, candle := range btc {
		signal, inserted, processErr := shadowEngine.process(ctx, candle, index == len(btc)-1)
		if processErr != nil || !inserted {
			t.Fatalf("BTCUSDT свеча %d не записана: %#v, %v", index, signal, processErr)
		}
		if index == len(btc)-1 && (signal.Action != domain.SignalBuy || !signal.EligibleForOrder || signal.OrderSent) {
			t.Fatalf("неожиданный разрешённый shadow-сигнал: %#v", signal)
		}
	}
	if _, _, err := controller.Activate(ctx, "проверка shadow-блокировки", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	eth := breakoutCandles("ETHUSDT", time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC))
	for index, candle := range eth {
		signal, inserted, processErr := shadowEngine.process(ctx, candle, index == len(eth)-1)
		if processErr != nil || !inserted {
			t.Fatalf("ETHUSDT свеча %d не записана: %#v, %v", index, signal, processErr)
		}
		if index == len(eth)-1 && (signal.Action != domain.SignalBuy || !signal.Blocked || signal.EligibleForOrder || signal.OrderSent) {
			t.Fatalf("kill switch не заблокировал shadow-сигнал: %#v", signal)
		}
	}

	if _, err := pool.Exec(ctx, `
		insert into tradingmaster_shadow_signals(
			symbol, interval, candle_time, close_price, action, reason, atr,
			in_position_before, eligible_for_order, blocked, order_sent
		) values ('BTCUSDT','1m',$1,100,'buy','проверка',1,false,true,false,true)`, btc[0].Candle.Time,
	); err == nil {
		t.Fatal("ограничение базы разрешило shadow-заявку")
	}
	if _, err := pool.Exec(ctx,
		"update tradingmaster_shadow_signals set reason = 'изменено' where symbol = 'BTCUSDT'",
	); err == nil || !strings.Contains(err.Error(), "только для добавления") {
		t.Fatalf("append-only защита shadow-журнала не сработала: %v", err)
	}
}

func breakoutCandles(symbol string, start time.Time) []MarketCandle {
	prices := [][5]float64{
		{100, 101, 99, 100, 1},
		{100, 102, 99, 101, 1.2},
		{101, 105, 100, 104, 1.5},
	}
	result := make([]MarketCandle, 0, len(prices))
	for index, value := range prices {
		openTime := start.Add(time.Duration(index) * time.Minute)
		result = append(result, MarketCandle{
			Symbol: symbol, Interval: "1m", CloseTime: openTime.Add(time.Minute - time.Millisecond),
			Candle: domain.Candle{
				Time: openTime, Open: value[0], High: value[1], Low: value[2], Close: value[3], Volume: value[4],
			},
		})
	}
	return result
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
		_, _ = connection.Exec(unlockContext, "select pg_advisory_unlock($1)", integrationTestLockID)
		connection.Release()
	}
}
