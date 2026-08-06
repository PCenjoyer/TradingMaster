package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PCenjoyer/TradingMaster/internal/alert"
)

func TestHealth(t *testing.T) {
	server := newTestServer(t, Config{})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "работает") {
		t.Fatalf("неожиданный ответ: %d %s", response.Code, response.Body.String())
	}
}

func TestBacktestRejectsUnknownFields(t *testing.T) {
	server := newTestServer(t, Config{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/backtests", bytes.NewBufferString("{\"unknown\":true}"))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("ожидался код 400, получен %d: %s", response.Code, response.Body.String())
	}
}

func TestSafetyAPIRequiresAdminToken(t *testing.T) {
	server := newTestServer(t, Config{AdminToken: "secret", DailyLossLimit: 0.03})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/safety", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("ожидался код 401, получен %d: %s", response.Code, response.Body.String())
	}
}

func TestDailyLossActivatesKillSwitchAndSendsAlert(t *testing.T) {
	notifier := &recordingNotifier{}
	server := newTestServer(t, Config{AdminToken: "secret", DailyLossLimit: 0.03, Notifier: notifier})
	handler := server.Handler()

	postAuthorized(t, handler, "/api/v1/safety/equity", `{"equity":10000,"observed_at":"2026-08-06T10:00:00Z"}`)
	response := postAuthorized(t, handler, "/api/v1/safety/equity", `{"equity":9600,"observed_at":"2026-08-06T11:00:00Z"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"daily_limit_active":true`) {
		t.Fatalf("дневной kill switch не активирован: %d %s", response.Code, response.Body.String())
	}
	if len(notifier.events) != 1 || notifier.events[0].Severity != alert.SeverityCritical {
		t.Fatalf("критичное уведомление не отправлено: %#v", notifier.events)
	}
}

func TestManualKillSwitchLifecycle(t *testing.T) {
	server := newTestServer(t, Config{AdminToken: "secret", DailyLossLimit: 0.03})
	handler := server.Handler()
	response := postAuthorized(t, handler, "/api/v1/safety/kill-switch", `{"action":"activate","reason":"проверка оператора"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"manual_active":true`) {
		t.Fatalf("kill switch не активирован: %d %s", response.Code, response.Body.String())
	}
	response = postAuthorized(t, handler, "/api/v1/safety/kill-switch", `{"action":"deactivate"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"active":false`) {
		t.Fatalf("kill switch не снят: %d %s", response.Code, response.Body.String())
	}
}

func TestNotificationFailureDoesNotRollbackKillSwitch(t *testing.T) {
	server := newTestServer(t, Config{
		AdminToken: "secret", DailyLossLimit: 0.03,
		Notifier: failingNotifier{},
	})
	response := postAuthorized(t, server.Handler(), "/api/v1/safety/kill-switch", `{"action":"activate","reason":"аварийная проверка"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"active":true`) {
		t.Fatalf("ошибка уведомления откатила kill switch: %d %s", response.Code, response.Body.String())
	}
}

type recordingNotifier struct {
	events []alert.Event
}

type failingNotifier struct{}

func (failingNotifier) Enabled() bool { return true }
func (failingNotifier) Notify(context.Context, alert.Event) error {
	return errors.New("тестовая ошибка доставки")
}

func (n *recordingNotifier) Enabled() bool { return true }
func (n *recordingNotifier) Notify(_ context.Context, event alert.Event) error {
	n.events = append(n.events, event)
	return nil
}

func newTestServer(t *testing.T, config Config) *Server {
	t.Helper()
	server, err := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", config)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func postAuthorized(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
