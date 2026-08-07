package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/domain"
	"github.com/coder/websocket"
)

type marketClient struct {
	restBase   string
	streamBase string
	httpClient *http.Client
	dial       func(context.Context, string, *websocket.DialOptions) (*websocket.Conn, *http.Response, error)
	now        func() time.Time
}

func newMarketClient() *marketClient {
	return &marketClient{
		restBase: liveMarketRESTURL, streamBase: liveMarketStreamURL,
		httpClient: &http.Client{Timeout: 15 * time.Second}, dial: websocket.Dial,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (client *marketClient) backfill(ctx context.Context, symbol, interval string, limit int) ([]MarketCandle, error) {
	endpoint, err := url.Parse(client.restBase)
	if err != nil {
		return nil, fmt.Errorf("разобрать адрес Binance market-data: %w", err)
	}
	endpoint.Path = "/api/v3/klines"
	query := endpoint.Query()
	query.Set("symbol", symbol)
	query.Set("interval", interval)
	query.Set("limit", strconv.Itoa(limit))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("получить историю публичных свечей: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("история публичных свечей вернула HTTP %d", response.StatusCode)
	}
	var payload [][]json.RawMessage
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("прочитать историю публичных свечей: %w", err)
	}
	now := client.now()
	result := make([]MarketCandle, 0, len(payload))
	for index, item := range payload {
		candle, err := parseRESTCandle(symbol, interval, item)
		if err != nil {
			return nil, fmt.Errorf("историческая свеча %d: %w", index, err)
		}
		if candle.CloseTime.After(now) {
			continue
		}
		result = append(result, candle)
	}
	return result, nil
}

func (client *marketClient) stream(
	ctx context.Context,
	symbols []string,
	interval string,
	handle func(time.Time, *MarketCandle) error,
) error {
	streamURL, err := buildStreamURL(client.streamBase, symbols, interval)
	if err != nil {
		return err
	}
	dialContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	connection, response, err := client.dial(dialContext, streamURL, &websocket.DialOptions{CompressionMode: websocket.CompressionDisabled})
	cancel()
	if err != nil {
		if response != nil {
			return fmt.Errorf("подключиться к Binance WebSocket: HTTP %d: %w", response.StatusCode, err)
		}
		return fmt.Errorf("подключиться к Binance WebSocket: %w", err)
	}
	defer connection.CloseNow()
	connection.SetReadLimit(1 << 20)

	for {
		messageType, body, err := connection.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("прочитать Binance WebSocket: %w", err)
		}
		if messageType != websocket.MessageText {
			continue
		}
		eventAt, candle, err := parseStreamMessage(body)
		if err != nil {
			return err
		}
		if err := handle(eventAt, candle); err != nil {
			return err
		}
	}
}

func buildStreamURL(base string, symbols []string, interval string) (string, error) {
	endpoint, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("разобрать адрес Binance WebSocket: %w", err)
	}
	if endpoint.Scheme != "wss" || endpoint.Host != "stream.binance.com:9443" || endpoint.Path != "" {
		return "", fmt.Errorf("разрешён только официальный публичный Binance WebSocket")
	}
	streams := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		streams = append(streams, strings.ToLower(symbol)+"@kline_"+interval)
	}
	endpoint.Path = "/stream"
	query := endpoint.Query()
	query.Set("streams", strings.Join(streams, "/"))
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func parseRESTCandle(symbol, interval string, values []json.RawMessage) (MarketCandle, error) {
	if len(values) < 7 {
		return MarketCandle{}, fmt.Errorf("ожидалось не менее 7 полей")
	}
	openTime, err := parseJSONInt(values[0])
	if err != nil {
		return MarketCandle{}, fmt.Errorf("open time: %w", err)
	}
	closeTime, err := parseJSONInt(values[6])
	if err != nil {
		return MarketCandle{}, fmt.Errorf("close time: %w", err)
	}
	prices := make([]float64, 5)
	for index, source := range values[1:6] {
		prices[index], err = parseJSONStringFloat(source)
		if err != nil {
			return MarketCandle{}, fmt.Errorf("поле цены %d: %w", index, err)
		}
	}
	result := MarketCandle{
		Symbol: symbol, Interval: interval,
		Candle: domain.Candle{
			Time: time.UnixMilli(openTime).UTC(), Open: prices[0], High: prices[1],
			Low: prices[2], Close: prices[3], Volume: prices[4],
		},
		CloseTime: time.UnixMilli(closeTime).UTC(),
	}
	if err := result.Candle.Validate(); err != nil {
		return MarketCandle{}, err
	}
	return result, nil
}

func parseStreamMessage(body []byte) (time.Time, *MarketCandle, error) {
	var payload struct {
		Data struct {
			EventType string `json:"e"`
			EventTime int64  `json:"E"`
			Symbol    string `json:"s"`
			Kline     struct {
				OpenTime           int64  `json:"t"`
				CloseTime          int64  `json:"T"`
				Interval           string `json:"i"`
				FirstTradeID       int64  `json:"f"`
				LastTradeID        int64  `json:"L"`
				Open               string `json:"o"`
				High               string `json:"h"`
				Low                string `json:"l"`
				Close              string `json:"c"`
				Volume             string `json:"v"`
				TradeCount         int64  `json:"n"`
				Closed             bool   `json:"x"`
				QuoteVolume        string `json:"q"`
				TakerBuyBaseVolume string `json:"V"`
				TakerBuyQuoteValue string `json:"Q"`
			} `json:"k"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return time.Time{}, nil, fmt.Errorf("прочитать сообщение Binance WebSocket: %w", err)
	}
	eventAt := time.UnixMilli(payload.Data.EventTime).UTC()
	if !payload.Data.Kline.Closed {
		return eventAt, nil, nil
	}
	values := []string{
		payload.Data.Kline.Open, payload.Data.Kline.High, payload.Data.Kline.Low,
		payload.Data.Kline.Close, payload.Data.Kline.Volume,
	}
	parsed := make([]float64, len(values))
	for index, raw := range values {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return time.Time{}, nil, fmt.Errorf("прочитать число WebSocket: %w", err)
		}
		parsed[index] = value
	}
	candle := &MarketCandle{
		Symbol: strings.ToUpper(payload.Data.Symbol), Interval: payload.Data.Kline.Interval,
		Candle: domain.Candle{
			Time: time.UnixMilli(payload.Data.Kline.OpenTime).UTC(), Open: parsed[0],
			High: parsed[1], Low: parsed[2], Close: parsed[3], Volume: parsed[4],
		},
		CloseTime: time.UnixMilli(payload.Data.Kline.CloseTime).UTC(),
	}
	if !shadowSymbolPattern.MatchString(candle.Symbol) {
		return time.Time{}, nil, fmt.Errorf("WebSocket вернул некорректный символ")
	}
	if _, ok := supportedIntervals[candle.Interval]; !ok {
		return time.Time{}, nil, fmt.Errorf("WebSocket вернул неподдерживаемый интервал")
	}
	if err := candle.Candle.Validate(); err != nil {
		return time.Time{}, nil, fmt.Errorf("WebSocket вернул некорректную свечу: %w", err)
	}
	return eventAt, candle, nil
}

func parseJSONInt(raw json.RawMessage) (int64, error) {
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func parseJSONStringFloat(raw json.RawMessage) (float64, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(text, 64)
}
