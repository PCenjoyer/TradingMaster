package shadow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestBuildStreamURLAllowsOnlyOfficialPublicBinance(t *testing.T) {
	streamURL, err := buildStreamURL(liveMarketStreamURL, []string{"BTCUSDT", "ETHUSDT"}, "1m")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(streamURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "wss" || parsed.Host != "stream.binance.com:9443" || parsed.Path != "/stream" {
		t.Fatalf("неожиданный адрес потока: %s", streamURL)
	}
	if got := parsed.Query().Get("streams"); got != "btcusdt@kline_1m/ethusdt@kline_1m" {
		t.Fatalf("неожиданный список потоков: %q", got)
	}
	if _, err := buildStreamURL("wss://example.com", []string{"BTCUSDT"}, "1m"); err == nil {
		t.Fatal("сторонний WebSocket endpoint должен быть запрещён")
	}
}

func TestParseClosedStreamMessage(t *testing.T) {
	body := []byte(`{"stream":"btcusdt@kline_1m","data":{"e":"kline","E":1786032060000,"s":"BTCUSDT","k":{"t":1786032000000,"T":1786032059999,"i":"1m","f":100,"L":120,"o":"100","h":"110","l":"99","c":"108","v":"2.5","n":21,"x":true,"q":"260","V":"1.1","Q":"118"}}}`)
	eventAt, candle, err := parseStreamMessage(body)
	if err != nil {
		t.Fatal(err)
	}
	if candle == nil || candle.Symbol != "BTCUSDT" || candle.Candle.Close != 108 {
		t.Fatalf("свеча не распознана: %#v", candle)
	}
	if eventAt.UnixMilli() != 1786032060000 {
		t.Fatalf("неожиданное время события: %s", eventAt)
	}

	partial := []byte(`{"data":{"E":1786032060000,"s":"BTCUSDT","k":{"x":false}}}`)
	_, candle, err = parseStreamMessage(partial)
	if err != nil || candle != nil {
		t.Fatalf("незакрытая свеча не должна обрабатываться: %#v, %v", candle, err)
	}
}

func TestBackfillKeepsOnlyClosedCandles(t *testing.T) {
	now := time.UnixMilli(1786032060000).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v3/klines" || request.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatalf("неожиданный запрос: %s", request.URL.String())
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[
			[1786031940000,"100","103","99","102","1.5",1786031999999],
			[1786032060000,"102","104","101","103","1.0",1786032119999]
		]`))
	}))
	defer server.Close()

	client := newMarketClient()
	client.restBase = server.URL
	client.httpClient = server.Client()
	client.now = func() time.Time { return now }
	candles, err := client.backfill(context.Background(), "BTCUSDT", "1m", 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 || candles[0].Candle.Close != 102 {
		t.Fatalf("ожидалась одна закрытая свеча: %#v", candles)
	}
}
