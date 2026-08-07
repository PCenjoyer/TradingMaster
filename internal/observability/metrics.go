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
	shadowEnabled     atomic.Uint32
	shadowConnected   atomic.Uint32
	shadowLeader      atomic.Uint32
	shadowLastEvent   atomic.Int64
	shadowCandles     atomic.Int64
	shadowSignals     atomic.Int64
	shadowReconnects  atomic.Int64
}

func (m *Metrics) SetShadow(
	enabled, connected, leader bool,
	lastEvent *time.Time,
	candles, signals, reconnects int64,
) {
	if enabled {
		m.shadowEnabled.Store(1)
	} else {
		m.shadowEnabled.Store(0)
	}
	if connected {
		m.shadowConnected.Store(1)
	} else {
		m.shadowConnected.Store(0)
	}
	if leader {
		m.shadowLeader.Store(1)
	} else {
		m.shadowLeader.Store(0)
	}
	if lastEvent != nil {
		m.shadowLastEvent.Store(lastEvent.Unix())
	}
	m.shadowCandles.Store(candles)
	m.shadowSignals.Store(signals)
	m.shadowReconnects.Store(reconnects)
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
			"tradingmaster_testnet_orders_total{status=\"error\"} %d\n"+
			"# HELP tradingmaster_shadow_enabled Включён ли безопасный shadow-режим.\n"+
			"# TYPE tradingmaster_shadow_enabled gauge\n"+
			"tradingmaster_shadow_enabled %d\n"+
			"# HELP tradingmaster_shadow_connected Подключён ли лидер к публичному Binance WebSocket.\n"+
			"# TYPE tradingmaster_shadow_connected gauge\n"+
			"tradingmaster_shadow_connected %d\n"+
			"# HELP tradingmaster_shadow_leader Является ли экземпляр лидером shadow-потока.\n"+
			"# TYPE tradingmaster_shadow_leader gauge\n"+
			"tradingmaster_shadow_leader %d\n"+
			"# HELP tradingmaster_shadow_last_event_timestamp_seconds Время последнего события публичного рынка.\n"+
			"# TYPE tradingmaster_shadow_last_event_timestamp_seconds gauge\n"+
			"tradingmaster_shadow_last_event_timestamp_seconds %d\n"+
			"# HELP tradingmaster_shadow_candles_processed Общее число сохранённых shadow-свечей.\n"+
			"# TYPE tradingmaster_shadow_candles_processed gauge\n"+
			"tradingmaster_shadow_candles_processed %d\n"+
			"# HELP tradingmaster_shadow_signals_generated Общее число рассчитанных shadow-сигналов.\n"+
			"# TYPE tradingmaster_shadow_signals_generated gauge\n"+
			"tradingmaster_shadow_signals_generated %d\n"+
			"# HELP tradingmaster_shadow_reconnects Общее число переподключений публичного market-data потока.\n"+
			"# TYPE tradingmaster_shadow_reconnects gauge\n"+
			"tradingmaster_shadow_reconnects %d\n",
		m.backtests.Load(), m.errors.Load(), m.duration.Load(), m.killSwitch.Load(),
		math.Float64frombits(m.dailyLoss.Load()), math.Float64frombits(m.dailyLimit.Load()),
		m.safetyTransitions.Load(), m.alertsSent.Load(), m.alertsFailed.Load(),
		m.durableStore.Load(), math.Float64frombits(m.paperEquity.Load()),
		m.paperFilled.Load(), m.paperRejected.Load(), m.paperErrors.Load(),
		m.testnetValidated.Load(), m.testnetSubmitted.Load(), m.testnetUnknown.Load(),
		m.testnetRejected.Load(), m.testnetErrors.Load(), m.shadowEnabled.Load(), m.shadowConnected.Load(),
		m.shadowLeader.Load(), m.shadowLastEvent.Load(), m.shadowCandles.Load(),
		m.shadowSignals.Load(), m.shadowReconnects.Load(),
	)
}
