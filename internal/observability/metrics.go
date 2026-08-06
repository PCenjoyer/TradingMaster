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
			"tradingmaster_telegram_notifications_total{status=\"failed\"} %d\n",
		m.backtests.Load(), m.errors.Load(), m.duration.Load(), m.killSwitch.Load(),
		math.Float64frombits(m.dailyLoss.Load()), math.Float64frombits(m.dailyLimit.Load()),
		m.safetyTransitions.Load(), m.alertsSent.Load(), m.alertsFailed.Load(),
	)
}
