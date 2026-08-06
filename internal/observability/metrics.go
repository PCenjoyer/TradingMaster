package observability

import (
	"fmt"
	"math"
	"net/http"
	"sync/atomic"
	"time"
)

type Metrics struct {
	backtests         atomic.Uint64
	errors            atomic.Uint64
	duration          atomic.Uint64
	killSwitch        atomic.Uint32
	dailyLoss         atomic.Uint64
	dailyLimit        atomic.Uint64
	safetyTransitions atomic.Uint64
	alertsSent        atomic.Uint64
	alertsFailed      atomic.Uint64
	durableStore      atomic.Uint32
	paperEquity       atomic.Uint64
	paperFilled       atomic.Uint64
	paperRejected     atomic.Uint64
	paperErrors       atomic.Uint64
	testnetValidated  atomic.Uint64
	testnetSubmitted  atomic.Uint64
	testnetUnknown    atomic.Uint64
	testnetRejected   atomic.Uint64
	testnetErrors     atomic.Uint64
}

func (m *Metrics) ObserveTestnetOrder(status string) {
	switch status {
	case "validated":
		m.testnetValidated.Add(1)
	case "submitted":
		m.testnetSubmitted.Add(1)
	case "unknown":
		m.testnetUnknown.Add(1)
	case "rejected":
		m.testnetRejected.Add(1)
	default:
		m.testnetErrors.Add(1)
	}
}

func (m *Metrics) SetSafetyUnavailable() {
	if previous := m.killSwitch.Swap(1); previous != 1 {
		m.safetyTransitions.Add(1)
	}
}

func (m *Metrics) SetDurableStore(enabled bool) {
	var value uint32
	if enabled {
		value = 1
	}
	m.durableStore.Store(value)
}

func (m *Metrics) SetPaperEquity(equity float64) {
	m.paperEquity.Store(math.Float64bits(equity))
}

func (m *Metrics) ObservePaperOrder(status string) {
	switch status {
	case "filled":
		m.paperFilled.Add(1)
	case "rejected":
		m.paperRejected.Add(1)
	default:
		m.paperErrors.Add(1)
	}
}

func (m *Metrics) SetSafety(active bool, dailyLoss, dailyLimit float64) {
	var value uint32
	if active {
		value = 1
	}
	if previous := m.killSwitch.Swap(value); previous != value {
		m.safetyTransitions.Add(1)
	}
	m.dailyLoss.Store(math.Float64bits(dailyLoss))
	m.dailyLimit.Store(math.Float64bits(dailyLimit))
}

func (m *Metrics) ObserveAlert(err error) {
	if err != nil {
		m.alertsFailed.Add(1)
		return
	}
	m.alertsSent.Add(1)
}

func (m *Metrics) ObserveBacktest(duration time.Duration, err error) {
	m.backtests.Add(1)
	m.duration.Add(uint64(duration.Milliseconds()))
	if err != nil {
		m.errors.Add(1)
	}
}

func (m *Metrics) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(writer,
		"# HELP tradingmaster_backtests_total Количество запущенных бэктестов.\n"+
			"# TYPE tradingmaster_backtests_total counter\n"+
			"tradingmaster_backtests_total %d\n"+
			"# HELP tradingmaster_backtest_errors_total Количество завершившихся ошибкой бэктестов.\n"+
			"# TYPE tradingmaster_backtest_errors_total counter\n"+
			"tradingmaster_backtest_errors_total %d\n"+
			"# HELP tradingmaster_backtest_duration_milliseconds_total Суммарное время бэктестов в миллисекундах.\n"+
			"# TYPE tradingmaster_backtest_duration_milliseconds_total counter\n"+
			"tradingmaster_backtest_duration_milliseconds_total %d\n"+
			"# HELP tradingmaster_kill_switch_active Активна ли блокировка новых торговых позиций.\n"+
			"# TYPE tradingmaster_kill_switch_active gauge\n"+
			"tradingmaster_kill_switch_active %d\n"+
			"# HELP tradingmaster_daily_loss_ratio Текущий дневной убыток относительно equity начала UTC-дня.\n"+
			"# TYPE tradingmaster_daily_loss_ratio gauge\n"+
			"tradingmaster_daily_loss_ratio %.8f\n"+
			"# HELP tradingmaster_daily_loss_limit_ratio Настроенный дневной лимит убытка.\n"+
			"# TYPE tradingmaster_daily_loss_limit_ratio gauge\n"+
			"tradingmaster_daily_loss_limit_ratio %.8f\n"+
			"# HELP tradingmaster_safety_transitions_total Количество переключений общего состояния kill switch.\n"+
			"# TYPE tradingmaster_safety_transitions_total counter\n"+
			"tradingmaster_safety_transitions_total %d\n"+
			"# HELP tradingmaster_telegram_notifications_total Результат отправки Telegram-уведомлений.\n"+
			"# TYPE tradingmaster_telegram_notifications_total counter\n"+
			"tradingmaster_telegram_notifications_total{status=\"sent\"} %d\n"+
			"tradingmaster_telegram_notifications_total{status=\"failed\"} %d\n"+
			"# HELP tradingmaster_safety_store_durable Используется ли общее durable-хранилище safety-state.\n"+
			"# TYPE tradingmaster_safety_store_durable gauge\n"+
			"tradingmaster_safety_store_durable %d\n"+
			"# HELP tradingmaster_paper_equity Текущая equity paper-портфеля.\n"+
			"# TYPE tradingmaster_paper_equity gauge\n"+
			"tradingmaster_paper_equity %.8f\n"+
			"# HELP tradingmaster_paper_orders_total Результаты обработки paper-заявок.\n"+
			"# TYPE tradingmaster_paper_orders_total counter\n"+
			"tradingmaster_paper_orders_total{status=\"filled\"} %d\n"+
			"tradingmaster_paper_orders_total{status=\"rejected\"} %d\n"+
			"tradingmaster_paper_orders_total{status=\"error\"} %d\n"+
			"# HELP tradingmaster_testnet_orders_total Результаты обработки заявок Binance Spot Testnet.\n"+
			"# TYPE tradingmaster_testnet_orders_total counter\n"+
			"tradingmaster_testnet_orders_total{status=\"validated\"} %d\n"+
			"tradingmaster_testnet_orders_total{status=\"submitted\"} %d\n"+
			"tradingmaster_testnet_orders_total{status=\"unknown\"} %d\n"+
			"tradingmaster_testnet_orders_total{status=\"rejected\"} %d\n"+
			"tradingmaster_testnet_orders_total{status=\"error\"} %d\n",
		m.backtests.Load(), m.errors.Load(), m.duration.Load(), m.killSwitch.Load(),
		math.Float64frombits(m.dailyLoss.Load()), math.Float64frombits(m.dailyLimit.Load()),
		m.safetyTransitions.Load(), m.alertsSent.Load(), m.alertsFailed.Load(),
		m.durableStore.Load(), math.Float64frombits(m.paperEquity.Load()),
		m.paperFilled.Load(), m.paperRejected.Load(), m.paperErrors.Load(),
		m.testnetValidated.Load(), m.testnetSubmitted.Load(), m.testnetUnknown.Load(),
		m.testnetRejected.Load(), m.testnetErrors.Load(),
	)
}
