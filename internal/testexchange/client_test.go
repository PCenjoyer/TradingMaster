package testexchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestBinanceClientSignsValidateOrderAndReadsAccount(t *testing.T) {
	const (
		apiKey    = "test-api-key"
		secretKey = "test-secret-key"
		serverNow = int64(1_754_500_000_000)
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v3/time":
			_, _ = fmt.Fprintf(writer, `{"serverTime":%d}`, serverNow)
		case "/api/v3/order/test":
			assertSignedRequest(t, request, apiKey, secretKey)
			if request.Method != http.MethodPost || request.URL.Query().Get("newClientOrderId") != "tm-client-order" {
				t.Fatalf("неожиданная тестовая заявка: %s %s", request.Method, request.URL.String())
			}
			_, _ = writer.Write([]byte(`{}`))
		case "/api/v3/account":
			assertSignedRequest(t, request, apiKey, secretKey)
			_, _ = writer.Write([]byte(`{
				"canTrade":true,"canDeposit":true,"canWithdraw":false,
				"updateTime":1754500000000,
				"balances":[{"asset":"USDT","free":"10000.00000000","locked":"0.00000000"}]
			}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	config := Config{
		APIKey: apiKey, SecretKey: secretKey, Mode: ModeValidate, ReceiveWindow: 5 * time.Second,
	}
	client, err := newBinanceClientAt(config, server.URL, true, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.submit(context.Background(), SubmitRequest{
		Symbol: "BTCUSDT", Side: SideBuy, OrderType: OrderTypeMarket, Quantity: "0.01",
	}, "tm-client-order", ModeValidate)
	if err != nil {
		t.Fatal(err)
	}
	account, err := client.account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !account.CanTrade || len(account.Balances) != 1 || account.Balances[0].Asset != "USDT" {
		t.Fatalf("неожиданный тестовый счёт: %#v", account)
	}
}

func TestProductionBinanceEndpointIsRejected(t *testing.T) {
	_, err := newBinanceClientAt(Config{
		APIKey: "api", SecretKey: "secret", Mode: ModeValidate,
	}, "https://api.binance.com", false, nil)
	if err == nil {
		t.Fatal("production endpoint не должен быть разрешён тестовому адаптеру")
	}
}

func TestNormalizeSubmitRequest(t *testing.T) {
	request, err := normalizeSubmitRequest(SubmitRequest{
		IdempotencyKey: "strategy:20260806:0001", Symbol: "btcusdt", Side: "BUY",
		OrderType: OrderTypeLimit, Quantity: "0.010", Price: "60000.50",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Symbol != "BTCUSDT" || request.Side != SideBuy {
		t.Fatalf("заявка не нормализована: %#v", request)
	}
	clientOrderID := deterministicClientOrderID(request.IdempotencyKey)
	if clientOrderID != deterministicClientOrderID(request.IdempotencyKey) ||
		clientOrderID == deterministicClientOrderID("strategy:20260806:0002") || len(clientOrderID) > 36 {
		t.Fatal("client order ID должен быть детерминированным")
	}
	if _, err := normalizeSubmitRequest(SubmitRequest{
		IdempotencyKey: "short", Symbol: "BTCUSDT", Side: SideBuy, Quantity: "0.01",
	}); err == nil {
		t.Fatal("короткий ключ идемпотентности должен быть отклонён")
	}
}

func assertSignedRequest(t *testing.T, request *http.Request, apiKey, secretKey string) {
	t.Helper()
	if request.Header.Get("X-MBX-APIKEY") != apiKey {
		t.Fatalf("API key не передан")
	}
	values := cloneValues(request.URL.Query())
	provided := values.Get("signature")
	values.Del("signature")
	signer := hmac.New(sha256.New, []byte(secretKey))
	_, _ = signer.Write([]byte(values.Encode()))
	expected := hex.EncodeToString(signer.Sum(nil))
	if !hmac.Equal([]byte(provided), []byte(expected)) {
		t.Fatalf("некорректная HMAC-подпись: %s", provided)
	}
}

func cloneValues(source url.Values) url.Values {
	clone := make(url.Values, len(source))
	for key, values := range source {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}
