package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
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
		NetworkId:                 networkID,
		Host:                      host,
		DefaultEthereumAddress:    cfg.EthereumAddress,
		StarkPublicKey:            cfg.StarkPublicKey,
		StarkPrivateKey:           cfg.StarkPrivateKey,
		StarkPublicKeyYCoordinate: cfg.StarkPublicKeyYCoordinate,
		ApiKeyCredentials:         &types.ApiKeyCredentials{Key: cfg.APIKey, Secret: cfg.APISecret, Passphrase: cfg.Passphrase},
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
	if interval == CandleIntervalTick {
		return c.subscribeTicks(ctx, symbol)
	}
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
	if interval == CandleIntervalTick {
		trades, err := c.getTickTrades(ctx, symbol, start, end)
		if err != nil {
			return nil, err
		}
		return tickCandles(symbol, trades), nil
	}
	if interval == CandleIntervalSecond {
		return c.getSecondCandles(ctx, symbol, start, end)
	}
	resolution, err := dydxResolution(interval)
	if err != nil {
		return nil, err
	}
	ranges, err := splitCandleRange(start, end, interval, 100)
	if err != nil {
		return nil, err
	}
	out := make([]Candle, 0)
	for _, r := range ranges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limit := estimateDydxLimit(r.Start, r.End, interval)
		req := &public.CandlesParam{
			Market:     symbol,
			Resolution: resolution,
		}
		if !r.Start.IsZero() {
			req.FromISO = r.Start.Format(time.RFC3339)
		}
		if !r.End.IsZero() {
			req.ToISO = r.End.Format(time.RFC3339)
		}
		if limit > 0 {
			req.Limit = limit
		}
		resp, err := c.client.Public.GetCandles(req)
		if err != nil {
			return nil, mapDydxError(err)
		}
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
	}
	if len(ranges) > 1 && len(out) > 1 {
		sort.Slice(out, func(i, j int) bool {
			return out[i].OpenTime.Before(out[j].OpenTime)
		})
	}
	return out, nil
}

func (c *dydxClient) getSecondCandles(ctx context.Context, symbol string, start, end time.Time) ([]Candle, error) {
	trades, err := c.getTickTrades(ctx, symbol, start, end)
	if err != nil {
		return nil, err
	}
	return aggregateSecondCandles(symbol, trades), nil
}

func (c *dydxClient) getTickTrades(ctx context.Context, symbol string, start, end time.Time) ([]tradeTick, error) {
	if symbol == "" {
		return nil, ErrInvalidConfig
	}
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if start.IsZero() {
		start = end.Add(-time.Second)
	}
	if end.Before(start) {
		return nil, ErrInvalidConfig
	}

	cursor := end
	trades := make([]tradeTick, 0)
	for i := 0; i < 100; i++ {
		param := &public.TradesParam{
			MarketID:           symbol,
			Limit:              100,
			StartingBeforeOrAt: cursor.Format(time.RFC3339),
		}
		resp, err := c.client.Public.GetTrades(param)
		if err != nil {
			return nil, mapDydxError(err)
		}
		if len(resp.Trades) == 0 {
			break
		}

		minTime := resp.Trades[0].CreatedAt
		for _, t := range resp.Trades {
			if t.CreatedAt.Before(minTime) {
				minTime = t.CreatedAt
			}
			if t.CreatedAt.Before(start) || t.CreatedAt.After(end) {
				continue
			}
			price, err := parseFloat(t.Price)
			if err != nil {
				continue
			}
			size, err := parseFloat(t.Size)
			if err != nil {
				continue
			}
			trades = append(trades, tradeTick{
				Time:  t.CreatedAt,
				Price: price,
				Size:  size,
			})
		}
		if minTime.Before(start) || len(resp.Trades) < 100 {
			break
		}
		cursor = minTime.Add(-time.Nanosecond)
	}

	return trades, nil
}

func (c *dydxClient) subscribeTicks(ctx context.Context, symbol string) (<-chan Candle, <-chan error) {
	if symbol == "" {
		return channelWithError(ErrInvalidConfig)
	}
	out := make(chan Candle)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		last := time.Time{}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			now := time.Now().UTC()
			start := last
			if start.IsZero() {
				start = now.Add(-time.Second)
			} else {
				start = start.Add(time.Millisecond)
			}
			trades, err := c.getTickTrades(ctx, symbol, start, now)
			if err != nil {
				select {
				case errs <- err:
				default:
				}
			} else if len(trades) > 0 {
				sort.Slice(trades, func(i, j int) bool {
					return trades[i].Time.Before(trades[j].Time)
				})
				for _, candle := range tickCandles(symbol, trades) {
					select {
					case out <- candle:
					case <-ctx.Done():
						return
					}
				}
				last = trades[len(trades)-1].Time
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out, errs
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

func (c *dydxClient) ListOrders(ctx context.Context, symbol string, status OrderStatus) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if symbol == "" {
		return nil, ErrInvalidConfig
	}
	param := &private.OrderQueryParam{}
	param.Market = symbol
	if status != "" {
		if filter, ok := dydxStatusFilter(status); ok {
			param.Status = filter
		}
	}
	resp, err := c.client.Private.GetOrders(param)
	if err != nil {
		return nil, mapDydxError(err)
	}
	out := make([]Order, 0, len(resp.Orders))
	for _, o := range resp.Orders {
		mapped := mapDydxOrder(o)
		if status != "" && mapped.Status != status {
			continue
		}
		out = append(out, mapped)
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
	if req.Type == OrderTypeLimit && req.Price == "" {
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
	if symbol == "" {
		return ErrInvalidConfig
	}
	_, err := c.client.Private.CancelOrder(orderID)
	return mapDydxError(err)
}

func (c *dydxClient) GetOrder(ctx context.Context, symbol, orderID string) (Order, error) {
	_ = symbol
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	if symbol == "" {
		return Order{}, ErrInvalidConfig
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
	case CandleIntervalMinute:
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
	status := mapDydxStatus(o.Status)
	if o.Size != "" && o.RemainingSize != "" {
		if size, err := parseFloat(o.Size); err == nil {
			if remaining, err := parseFloat(o.RemainingSize); err == nil {
				if size >= remaining {
					diff := size - remaining
					filled = formatFloat(diff)
					if status == OrderStatusNew && remaining > 0 && diff > 0 {
						status = OrderStatusPartiallyFilled
					}
				}
			}
		}
	}
	return Order{
		ID:        o.ID,
		Symbol:    o.Market,
		Side:      mapDydxSideFromAPI(o.Side),
		Type:      mapDydxTypeFromAPI(o.Type),
		Status:    status,
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

func dydxStatusFilter(status OrderStatus) (string, bool) {
	switch status {
	case OrderStatusNew, OrderStatusPartiallyFilled:
		return types.OrderStatusOpen, true
	case OrderStatusFilled:
		return types.OrderStatusFilled, true
	case OrderStatusCanceled:
		return types.OrderStatusCanceled, true
	default:
		return "", false
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
