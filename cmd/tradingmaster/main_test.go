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

func TestPaperConfigFromEnvironment(t *testing.T) {
	t.Setenv("TM_PAPER_INITIAL_CAPITAL", "25000")
	t.Setenv("TM_PAPER_FEE_BPS", "8")
	t.Setenv("TM_PAPER_SLIPPAGE_BPS", "3")
	config, err := paperConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.InitialCapital != 25_000 || config.FeeBPS != 8 || config.SlippageBPS != 3 {
		t.Fatalf("неожиданная paper-конфигурация: %#v", config)
	}
}

func TestTestnetConfigFromEnvironment(t *testing.T) {
	t.Setenv("TM_BINANCE_TESTNET_API_KEY", "test-api")
	t.Setenv("TM_BINANCE_TESTNET_SECRET_KEY", "test-secret")
	t.Setenv("TM_BINANCE_TESTNET_ORDER_MODE", "execute")
	t.Setenv("TM_BINANCE_TESTNET_RECV_WINDOW_MS", "4000")
	config, err := testnetConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.APIKey != "test-api" || config.SecretKey != "test-secret" ||
		config.Mode != "execute" || config.ReceiveWindow.Milliseconds() != 4000 {
		t.Fatalf("неожиданная testnet-конфигурация: %#v", config)
	}
}

func TestTestnetReceiveWindowMustBeInteger(t *testing.T) {
	t.Setenv("TM_BINANCE_TESTNET_RECV_WINDOW_MS", "1.5")
	if _, err := testnetConfigFromEnv(); err == nil {
		t.Fatal("дробный receive window должен быть отклонён")
	}
}
