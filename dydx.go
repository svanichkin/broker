package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-numb/go-dydx"
	"github.com/go-numb/go-dydx/helpers"
	"github.com/go-numb/go-dydx/private"
	"github.com/go-numb/go-dydx/public"
	"github.com/go-numb/go-dydx/types"
)

const dydxMainnetURL = "https://api.dydx.exchange"

type dydxClient struct {
	client     *dydx.Client
	httpClient *http.Client
	cfg        Config
	host       string
}

func newDydx(cfg Config) (*dydxClient, error) {
	host := cfg.BaseURL
	if host == "" {
		host = dydxMainnetURL
	}
	networkID := types.NetworkIdMainnet
	if strings.Contains(host, "stage") || strings.Contains(host, "ropsten") {
		networkID = types.NetworkIdRopsten
	}
	options := types.Options{
		NetworkId:                   networkID,
		Host:                        host,
		DefaultEthereumAddress:      cfg.EthereumAddress,
		StarkPublicKey:              cfg.StarkPublicKey,
		StarkPrivateKey:             cfg.StarkPrivateKey,
		StarkPublicKeyYCoordinate:   cfg.StarkPublicKeyYCoordinate,
		ApiKeyCredentials:           &types.ApiKeyCredentials{Key: cfg.APIKey, Secret: cfg.APISecret, Passphrase: cfg.Passphrase},
	}
	client := dydx.New(options)
	client.Logger = nil
	return &dydxClient{
		client:     client,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		cfg:        cfg,
		host:       host,
	}, nil
}

func (c *dydxClient) Name() ExchangeName {
	return ExchangeDydx
}

func (c *dydxClient) Capabilities() Capabilities {
	return Capabilities{Spot: false, Derivatives: true, Streaming: true}
}

func (c *dydxClient) SubscribeCandles(ctx context.Context, symbol string, interval CandleInterval) (<-chan Candle, <-chan error) {
	dur, err := intervalDuration(interval)
	if err != nil {
		return channelWithError(err)
	}
	return subscribeByPolling(ctx, dur, func(ctx context.Context) (Candle, error) {
		end := time.Now()
		start := end.Add(-dur)
		candles, err := c.GetCandles(ctx, symbol, interval, start, end)
		if err != nil {
			return Candle{}, err
		}
		if len(candles) == 0 {
			return Candle{}, ErrNotSupported
		}
		return candles[len(candles)-1], nil
	})
}

func (c *dydxClient) GetCandles(ctx context.Context, symbol string, interval CandleInterval, start, end time.Time) ([]Candle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolution, err := dydxResolution(interval)
	if err != nil {
		return nil, err
	}
	limit := estimateDydxLimit(start, end, interval)
	req := &public.CandlesParam{
		Market:     symbol,
		Resolution: resolution,
	}
	if !start.IsZero() {
		req.FromISO = start.Format(time.RFC3339)
	}
	if !end.IsZero() {
		req.ToISO = end.Format(time.RFC3339)
	}
	if limit > 0 {
		req.Limit = limit
	}
	resp, err := c.client.Public.GetCandles(req)
	if err != nil {
		return nil, mapDydxError(err)
	}
	out := make([]Candle, 0, len(resp.Candles))
	for _, k := range resp.Candles {
		out = append(out, Candle{
			Symbol:    symbol,
			Interval:  interval,
			OpenTime:  k.StartedAt,
			CloseTime: k.UpdatedAt,
			Open:      k.Open,
			High:      k.High,
			Low:       k.Low,
			Close:     k.Close,
			Volume:    k.UsdVolume,
			Trades:    k.Trades,
		})
	}
	return out, nil
}

func (c *dydxClient) GetBalances(ctx context.Context) ([]Balance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	account, err := c.getAccount(ctx)
	if err != nil {
		return nil, err
	}
	free := account.FreeCollateral
	total := account.Equity
	locked := ""
	if total != "" && free != "" {
		if tf, err := parseFloat(total); err == nil {
			if ff, err := parseFloat(free); err == nil {
				locked = formatFloat(tf - ff)
			}
		}
	}
	return []Balance{{
		Asset:  "USDC",
		Free:   free,
		Locked: locked,
		Total:  total,
	}}, nil
}

func (c *dydxClient) ListOpenOrders(ctx context.Context, symbol string) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	param := &private.OrderQueryParam{Status: types.OrderStatusOpen}
	if symbol != "" {
		param.Market = symbol
	}
	resp, err := c.client.Private.GetOrders(param)
	if err != nil {
		return nil, mapDydxError(err)
	}
	out := make([]Order, 0, len(resp.Orders))
	for _, o := range resp.Orders {
		out = append(out, mapDydxOrder(o))
	}
	return out, nil
}

func (c *dydxClient) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	if req.Symbol == "" || req.Quantity == "" {
		return Order{}, ErrInvalidConfig
	}
	positionID, err := c.positionID(ctx)
	if err != nil {
		return Order{}, err
	}
	orderType := types.LIMIT
	if req.Type == OrderTypeMarket {
		orderType = types.MARKET
	}
	timeInForce := types.TimeInForceGtt
	switch req.TimeInForce {
	case TimeInForceIOC:
		timeInForce = types.TimeInForceIoc
	case TimeInForceFOK:
		timeInForce = types.TimeInForceFok
	}
	input := &private.ApiOrder{
		ApiBaseOrder: private.ApiBaseOrder{Expiration: helpers.ExpireAfter(5 * time.Minute)},
		Market:       req.Symbol,
		Side:         mapDydxSide(req.Side),
		Type:         orderType,
		Size:         req.Quantity,
		Price:        req.Price,
		ClientId:     req.ClientOrderID,
		TimeInForce:  timeInForce,
		LimitFee:     "0.0015",
		PostOnly:     false,
	}
	resp, err := c.client.Private.CreateOrder(input, positionID)
	if err != nil {
		return Order{}, mapDydxError(err)
	}
	return mapDydxOrder(resp.Order), nil
}

func (c *dydxClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	_ = symbol
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.client.Private.CancelOrder(orderID)
	return mapDydxError(err)
}

func (c *dydxClient) GetOrder(ctx context.Context, symbol, orderID string) (Order, error) {
	_ = symbol
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	resp, err := c.client.Private.GetOrderById(orderID)
	if err != nil {
		return Order{}, mapDydxError(err)
	}
	return mapDydxOrder(resp.Order), nil
}

func (c *dydxClient) Ping(ctx context.Context) error {
	_, err := c.ServerTime(ctx)
	return err
}

func (c *dydxClient) ServerTime(ctx context.Context) (time.Time, error) {
	type timeResponse struct {
		ISO   string `json:"iso"`
		Epoch int64  `json:"epoch"`
	}
	url := strings.TrimRight(c.host, "/") + "/v3/time"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return time.Time{}, mapDydxError(err)
	}
	defer resp.Body.Close()
	var payload timeResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return time.Time{}, err
	}
	if payload.ISO != "" {
		if t, err := time.Parse(time.RFC3339Nano, payload.ISO); err == nil {
			return t, nil
		}
		if t, err := time.Parse(time.RFC3339, payload.ISO); err == nil {
			return t, nil
		}
	}
	if payload.Epoch > 0 {
		return time.Unix(payload.Epoch, 0), nil
	}
	return time.Time{}, ErrNotSupported
}

func (c *dydxClient) getAccount(ctx context.Context) (private.Account, error) {
	if c.cfg.EthereumAddress == "" {
		return private.Account{}, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return private.Account{}, err
	}
	resp, err := c.client.Private.GetAccount(c.cfg.EthereumAddress)
	if err != nil {
		return private.Account{}, mapDydxError(err)
	}
	return resp.Account, nil
}

func (c *dydxClient) positionID(ctx context.Context) (int64, error) {
	account, err := c.getAccount(ctx)
	if err != nil {
		return 0, err
	}
	return account.PositionId, nil
}

func dydxResolution(interval CandleInterval) (string, error) {
	switch interval {
	case CandleIntervalTick, CandleIntervalMinute:
		return types.Resolution1MIN, nil
	case CandleIntervalHour:
		return types.Resolution1HOUR, nil
	case CandleIntervalDay:
		return types.Resolution1D, nil
	default:
		return "", ErrNotSupported
	}
}

func mapDydxOrder(o private.Order) Order {
	filled := ""
	if o.Size != "" && o.RemainingSize != "" {
		if size, err := parseFloat(o.Size); err == nil {
			if remaining, err := parseFloat(o.RemainingSize); err == nil {
				filled = formatFloat(size - remaining)
			}
		}
	}
	return Order{
		ID:        o.ID,
		Symbol:    o.Market,
		Side:      mapDydxSideFromAPI(o.Side),
		Type:      mapDydxTypeFromAPI(o.Type),
		Status:    mapDydxStatus(o.Status),
		Quantity:  o.Size,
		Filled:    filled,
		Price:     o.Price,
		AvgPrice:  "",
		CreatedAt: o.CreatedAt,
		UpdatedAt: o.ExpiresAt,
	}
}

func mapDydxStatus(status string) OrderStatus {
	switch strings.ToUpper(status) {
	case types.OrderStatusOpen, types.OrderStatusPending:
		return OrderStatusNew
	case types.OrderStatusFilled:
		return OrderStatusFilled
	case types.OrderStatusCanceled:
		return OrderStatusCanceled
	case types.OrderStatusUntriggered:
		return OrderStatusNew
	default:
		return OrderStatusRejected
	}
}

func mapDydxSide(side OrderSide) string {
	if side == OrderSideSell {
		return types.SELL
	}
	return types.BUY
}

func mapDydxSideFromAPI(side string) OrderSide {
	if strings.ToUpper(side) == types.SELL {
		return OrderSideSell
	}
	return OrderSideBuy
}

func mapDydxTypeFromAPI(orderType string) OrderType {
	if strings.ToUpper(orderType) == types.MARKET {
		return OrderTypeMarket
	}
	return OrderTypeLimit
}

func mapDydxError(err error) error {
	return mapCommonError(err)
}

func estimateDydxLimit(start, end time.Time, interval CandleInterval) int {
	dur, err := intervalDuration(interval)
	if err != nil || dur == 0 {
		return 0
	}
	if start.IsZero() || end.IsZero() {
		return 0
	}
	if end.Before(start) {
		return 1
	}
	count := int(end.Sub(start)/dur) + 1
	if count < 1 {
		return 1
	}
	if count > 100 {
		return 100
	}
	return count
}
