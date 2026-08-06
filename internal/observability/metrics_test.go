package observability

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSafetyAndTelegramMetrics(t *testing.T) {
	metrics := &Metrics{}
	metrics.SetSafety(true, 0.025, 0.03)
	metrics.ObserveAlert(nil)
	metrics.ObserveAlert(errors.New("ошибка доставки"))
	metrics.SetDurableStore(true)
	metrics.SetPaperEquity(12_345.67)
	metrics.ObservePaperOrder("filled")
	metrics.ObservePaperOrder("rejected")
	metrics.ObserveTestnetOrder("validated")
	metrics.ObserveTestnetOrder("unknown")

	response := httptest.NewRecorder()
	metrics.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		"tradingmaster_kill_switch_active 1",
		"tradingmaster_daily_loss_ratio 0.02500000",
		"tradingmaster_daily_loss_limit_ratio 0.03000000",
		"tradingmaster_telegram_notifications_total{status=\"sent\"} 1",
		"tradingmaster_telegram_notifications_total{status=\"failed\"} 1",
		"tradingmaster_safety_store_durable 1",
		"tradingmaster_paper_equity 12345.67000000",
		"tradingmaster_paper_orders_total{status=\"filled\"} 1",
		"tradingmaster_paper_orders_total{status=\"rejected\"} 1",
		"tradingmaster_testnet_orders_total{status=\"validated\"} 1",
		"tradingmaster_testnet_orders_total{status=\"unknown\"} 1",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("в метриках нет %q:\n%s", expected, body)
		}
	}
}
