package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	server := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "работает") {
		t.Fatalf("неожиданный ответ: %d %s", response.Code, response.Body.String())
	}
}

func TestBacktestRejectsUnknownFields(t *testing.T) {
	server := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/backtests", bytes.NewBufferString("{\"unknown\":true}"))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("ожидался код 400, получен %d: %s", response.Code, response.Body.String())
	}
}
