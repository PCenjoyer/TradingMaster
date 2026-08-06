package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := healthcheck(server.URL); err != nil {
		t.Fatalf("проверка здоровья завершилась ошибкой: %v", err)
	}
}

func TestHealthcheckRejectsUnhealthyService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if err := healthcheck(server.URL); err == nil {
		t.Fatal("ожидалась ошибка нездорового сервиса")
	}
}

func TestNotifierRequiresBothTelegramSettings(t *testing.T) {
	t.Setenv("TM_TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TM_TELEGRAM_CHAT_ID", "")
	if _, err := notifierFromEnv(); err == nil {
		t.Fatal("ожидалась ошибка неполной Telegram-конфигурации")
	}
}

func TestEnvironmentParsers(t *testing.T) {
	t.Setenv("TM_DAILY_LOSS_LIMIT", "0.025")
	value, err := envFloat("TM_DAILY_LOSS_LIMIT", 0.03)
	if err != nil || value != 0.025 {
		t.Fatalf("не распознан float: %f, %v", value, err)
	}
	t.Setenv("TM_KILL_SWITCH_DEFAULT", "true")
	active, err := envBool("TM_KILL_SWITCH_DEFAULT", false)
	if err != nil || !active {
		t.Fatalf("не распознан bool: %t, %v", active, err)
	}
}
