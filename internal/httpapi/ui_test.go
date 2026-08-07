package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestUIRedirectsUnauthenticatedUserToLogin(t *testing.T) {
	server := newTestServer(t, Config{AdminToken: "secret"})
	request := httptest.NewRequest(http.MethodGet, "/ui", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/ui/login" {
		t.Fatalf("ожидалось перенаправление на вход, получено %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestUILoginCreatesProtectedSessionWithoutStoringAdminToken(t *testing.T) {
	server := newTestServer(t, Config{AdminToken: "secret"})
	form := url.Values{"token": {"secret"}}
	request := httptest.NewRequest(http.MethodPost, "/ui/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/ui" {
		t.Fatalf("вход не выполнен: %d %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("ожидалась одна cookie, получено %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != uiSessionCookie || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("небезопасные параметры cookie: %#v", cookie)
	}
	if strings.Contains(cookie.Value, "secret") {
		t.Fatal("admin token не должен сохраняться в cookie")
	}
}

func TestUISessionAuthorizesReadsAndRequiresCSRFForWrites(t *testing.T) {
	server := newTestServer(t, Config{AdminToken: "secret"})
	cookie := loginUICookie(t, server)
	handler := server.Handler()

	read := httptest.NewRequest(http.MethodGet, "/api/v1/safety", nil)
	read.AddCookie(cookie)
	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("UI-сессия не авторизовала чтение: %d %s", readResponse.Code, readResponse.Body.String())
	}

	write := httptest.NewRequest(http.MethodPost, "/api/v1/safety/kill-switch", bytes.NewBufferString(`{"action":"deactivate"}`))
	write.AddCookie(cookie)
	writeResponse := httptest.NewRecorder()
	handler.ServeHTTP(writeResponse, write)
	if writeResponse.Code != http.StatusForbidden {
		t.Fatalf("запись без CSRF должна быть запрещена: %d %s", writeResponse.Code, writeResponse.Body.String())
	}

	claims, ok := server.ui.session(read)
	if !ok {
		t.Fatal("не удалось прочитать тестовую UI-сессию")
	}
	write = httptest.NewRequest(http.MethodPost, "/api/v1/safety/kill-switch", bytes.NewBufferString(`{"action":"deactivate"}`))
	write.AddCookie(cookie)
	write.Header.Set("X-CSRF-Token", claims.CSRF)
	writeResponse = httptest.NewRecorder()
	handler.ServeHTTP(writeResponse, write)
	if writeResponse.Code != http.StatusOK {
		t.Fatalf("запись с CSRF не выполнена: %d %s", writeResponse.Code, writeResponse.Body.String())
	}
}

func TestUIDashboardIsRussianAndProtectedByCSP(t *testing.T) {
	server := newTestServer(t, Config{AdminToken: "secret"})
	request := httptest.NewRequest(http.MethodGet, "/ui", nil)
	request.AddCookie(loginUICookie(t, server))
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("панель не открылась: %d %s", response.Code, response.Body.String())
	}
	for _, phrase := range []string{"Панель управления", "Реальная торговля отключена", "Binance Spot Testnet"} {
		if !strings.Contains(response.Body.String(), phrase) {
			t.Fatalf("на странице нет текста %q", phrase)
		}
	}
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("CSP не настроена: %q", response.Header().Get("Content-Security-Policy"))
	}
}

func TestUITamperedSessionIsRejected(t *testing.T) {
	server := newTestServer(t, Config{AdminToken: "secret"})
	cookie := loginUICookie(t, server)
	cookie.Value += "tampered"
	request := httptest.NewRequest(http.MethodGet, "/api/v1/safety", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("повреждённая сессия должна быть отклонена: %d %s", response.Code, response.Body.String())
	}
}

func TestUILoginIsDisabledWithoutAdminToken(t *testing.T) {
	server := newTestServer(t, Config{})
	request := httptest.NewRequest(http.MethodPost, "/ui/login", strings.NewReader("token=anything"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "TM_ADMIN_TOKEN") {
		t.Fatalf("ожидалась инструкция по настройке токена: %d %s", response.Code, response.Body.String())
	}
}

func loginUICookie(t *testing.T, server *Server) *http.Cookie {
	t.Helper()
	form := url.Values{"token": {"secret"}}
	request := httptest.NewRequest(http.MethodPost, "/ui/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("не удалось войти в UI: %d %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("сервер не выдал session cookie")
	}
	return cookies[0]
}
