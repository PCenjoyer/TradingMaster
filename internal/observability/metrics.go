package observability

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

type Metrics struct {
	backtests atomic.Uint64
	errors    atomic.Uint64
	duration  atomic.Uint64
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
			"tradingmaster_backtest_duration_milliseconds_total %d\n",
		m.backtests.Load(), m.errors.Load(), m.duration.Load(),
	)
}
