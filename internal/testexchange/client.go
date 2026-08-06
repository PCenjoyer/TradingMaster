package testexchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const binanceSpotTestnetURL = "https://testnet.binance.vision"

type apiError struct {
	HTTPStatus int
	Code       int
	Message    string
	ambiguous  bool
}

func (e *apiError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("Binance Spot Testnet: код %d: %s", e.Code, e.Message)
	}
	if e.HTTPStatus == 0 {
		return "Binance Spot Testnet: " + e.Message
	}
	return fmt.Sprintf("Binance Spot Testnet: HTTP %d: %s", e.HTTPStatus, e.Message)
}

func (e *apiError) Ambiguous() bool { return e.ambiguous }

type remoteOrder struct {
	Symbol             string `json:"symbol"`
	OrderID            int64  `json:"orderId"`
	ClientOrderID      string `json:"clientOrderId"`
	TransactTime       int64  `json:"transactTime"`
	Status             string `json:"status"`
	ExecutedQuantity   string `json:"executedQty"`
	CumulativeQuoteQty string `json:"cummulativeQuoteQty"`
}

type binanceClient struct {
	endpoint      *url.URL
	apiKey        string
	secretKey     string
	receiveWindow int64
	httpClient    *http.Client
}

func newBinanceClient(config Config) (*binanceClient, error) {
	return newBinanceClientAt(config, binanceSpotTestnetURL, false, nil)
}

func newBinanceClientAt(config Config, rawEndpoint string, allowLocal bool, httpClient *http.Client) (*binanceClient, error) {
	if strings.TrimSpace(config.APIKey) == "" || strings.TrimSpace(config.SecretKey) == "" {
		return nil, fmt.Errorf("API key и secret key Binance Spot Testnet обязательны")
	}
	endpoint, err := url.Parse(rawEndpoint)
	if err != nil {
		return nil, fmt.Errorf("разобрать адрес Binance Spot Testnet: %w", err)
	}
	if !allowLocal {
		if endpoint.Scheme != "https" || endpoint.Host != "testnet.binance.vision" ||
			endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.User != nil {
			return nil, fmt.Errorf("разрешён только официальный адрес Binance Spot Testnet")
		}
	}
	receiveWindow := config.ReceiveWindow
	if receiveWindow == 0 {
		receiveWindow = 5 * time.Second
	}
	if receiveWindow < time.Millisecond || receiveWindow > 60*time.Second {
		return nil, fmt.Errorf("receive window должен быть от 1 до 60000 миллисекунд")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 12 * time.Second}
	}
	return &binanceClient{
		endpoint: endpoint, apiKey: strings.TrimSpace(config.APIKey), secretKey: config.SecretKey,
		receiveWindow: receiveWindow.Milliseconds(), httpClient: httpClient,
	}, nil
}

func (c *binanceClient) status(ctx context.Context, mode Mode) (ServiceStatus, error) {
	serverTime, err := c.serverTime(ctx)
	if err != nil {
		return ServiceStatus{}, err
	}
	return ServiceStatus{
		Connected: true, Exchange: "Binance Spot", Environment: "testnet",
		Mode: mode, ServerTime: time.UnixMilli(serverTime).UTC(),
	}, nil
}

func (c *binanceClient) serverTime(ctx context.Context) (int64, error) {
	var payload struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v3/time", nil, false, &payload); err != nil {
		return 0, err
	}
	if payload.ServerTime <= 0 {
		return 0, fmt.Errorf("Binance Spot Testnet вернул некорректное серверное время")
	}
	return payload.ServerTime, nil
}

func (c *binanceClient) account(ctx context.Context) (Account, error) {
	serverTime, err := c.serverTime(ctx)
	if err != nil {
		return Account{}, err
	}
	var payload struct {
		CanTrade    bool      `json:"canTrade"`
		CanDeposit  bool      `json:"canDeposit"`
		CanWithdraw bool      `json:"canWithdraw"`
		UpdateTime  int64     `json:"updateTime"`
		Balances    []Balance `json:"balances"`
	}
	params := url.Values{"omitZeroBalances": {"true"}}
	if err := c.doSigned(ctx, http.MethodGet, "/api/v3/account", params, serverTime, &payload); err != nil {
		return Account{}, err
	}
	updatedAt := time.UnixMilli(payload.UpdateTime).UTC()
	if payload.UpdateTime == 0 {
		updatedAt = time.UnixMilli(serverTime).UTC()
	}
	return Account{
		CanTrade: payload.CanTrade, CanDeposit: payload.CanDeposit, CanWithdraw: payload.CanWithdraw,
		UpdatedAt: updatedAt, Balances: payload.Balances,
	}, nil
}

func (c *binanceClient) submit(ctx context.Context, request SubmitRequest, clientOrderID string, mode Mode) (remoteOrder, error) {
	serverTime, err := c.serverTime(ctx)
	if err != nil {
		return remoteOrder{}, err
	}
	params := url.Values{
		"symbol":           {request.Symbol},
		"side":             {strings.ToUpper(string(request.Side))},
		"type":             {strings.ToUpper(string(request.OrderType))},
		"quantity":         {request.Quantity},
		"newClientOrderId": {clientOrderID},
		"newOrderRespType": {"RESULT"},
	}
	if request.OrderType == OrderTypeLimit {
		params.Set("price", request.Price)
		params.Set("timeInForce", "GTC")
	}
	path := "/api/v3/order/test"
	if mode == ModeExecute {
		path = "/api/v3/order"
	}
	var result remoteOrder
	if err := c.doSigned(ctx, http.MethodPost, path, params, serverTime, &result); err != nil {
		return remoteOrder{}, err
	}
	return result, nil
}

func (c *binanceClient) queryOrder(ctx context.Context, symbol, clientOrderID string) (remoteOrder, error) {
	serverTime, err := c.serverTime(ctx)
	if err != nil {
		return remoteOrder{}, err
	}
	params := url.Values{"symbol": {symbol}, "origClientOrderId": {clientOrderID}}
	var result remoteOrder
	if err := c.doSigned(ctx, http.MethodGet, "/api/v3/order", params, serverTime, &result); err != nil {
		return remoteOrder{}, err
	}
	return result, nil
}

func (c *binanceClient) doSigned(
	ctx context.Context,
	method, path string,
	params url.Values,
	serverTime int64,
	destination any,
) error {
	params.Set("recvWindow", strconv.FormatInt(c.receiveWindow, 10))
	params.Set("timestamp", strconv.FormatInt(serverTime, 10))
	payload := params.Encode()
	signer := hmac.New(sha256.New, []byte(c.secretKey))
	_, _ = signer.Write([]byte(payload))
	params.Set("signature", hex.EncodeToString(signer.Sum(nil)))
	return c.do(ctx, method, path, params, true, destination)
}

func (c *binanceClient) do(
	ctx context.Context,
	method, path string,
	params url.Values,
	authenticated bool,
	destination any,
) error {
	requestURL := *c.endpoint
	requestURL.Path = path
	if params != nil {
		requestURL.RawQuery = params.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("создать запрос Binance Spot Testnet: %w", err)
	}
	if authenticated {
		request.Header.Set("X-MBX-APIKEY", c.apiKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return &apiError{Message: "сетевой запрос завершился неопределённо", ambiguous: true}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return &apiError{HTTPStatus: response.StatusCode, Message: "ответ не прочитан", ambiguous: response.StatusCode >= 500}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.Unmarshal(body, &payload)
		message := strings.TrimSpace(payload.Msg)
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return &apiError{
			HTTPStatus: response.StatusCode, Code: payload.Code, Message: message,
			ambiguous: response.StatusCode >= 500,
		}
	}
	if destination == nil || len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("разобрать ответ Binance Spot Testnet: %w", err)
	}
	return nil
}

func extractAPIError(err error) (*apiError, bool) {
	var target *apiError
	ok := errors.As(err, &target)
	return target, ok
}
