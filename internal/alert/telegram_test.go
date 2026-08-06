package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelegramSendsRussianAlert(t *testing.T) {
	var received struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/bottest-token/sendMessage" {
			t.Fatalf("неожиданный Telegram-запрос: %s %s", request.Method, request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier, err := NewTelegram(TelegramConfig{
		BotToken: "test-token",
		ChatID:   "42",
		BaseURL:  server.URL,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = notifier.Notify(context.Background(), Event{
		Severity: SeverityCritical,
		Title:    "Торговля остановлена",
		Message:  "Превышен дневной лимит",
		Time:     time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.ChatID != "42" || !strings.Contains(received.Text, "КРИТИЧНО") || !strings.Contains(received.Text, "дневной лимит") {
		t.Fatalf("неожиданное уведомление: %#v", received)
	}
}

func TestTelegramDoesNotExposeResponseBodyOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "секретный текст", http.StatusUnauthorized)
	}))
	defer server.Close()
	notifier, err := NewTelegram(TelegramConfig{BotToken: "token", ChatID: "42", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = notifier.Notify(context.Background(), Event{Title: "Проверка"})
	if err == nil || strings.Contains(err.Error(), "секретный текст") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
}

func TestTelegramDoesNotExposeTokenOnNetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close()

	notifier, err := NewTelegram(TelegramConfig{
		BotToken: "super-secret-token", ChatID: "42",
		BaseURL: serverURL, Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = notifier.Notify(context.Background(), Event{Title: "Проверка"})
	if err == nil || strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("небезопасная ошибка: %v", err)
	}
}
