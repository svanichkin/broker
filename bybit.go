package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	bybit "github.com/bybit-exchange/bybit.go.api"
	"github.com/bybit-exchange/bybit.go.api/models"
)

const (
	bybitMainnetURL      = bybit.MAINNET
	bybitDefaultCategory = "linear"
	bybitDefaultAccount  = "UNIFIED"
)

type bybitClient struct {
	client      *bybit.Client
	cfg         Config
	category    string
	accountType string
}

func newBybit(cfg Config) (*bybitClient, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = bybitMainnetURL
	}
	client := bybit.NewBybitHttpClient(cfg.APIKey, cfg.APISecret, bybit.WithBaseURL(baseURL))
	client.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	return &bybitClient{
		client:      client,
		cfg:         cfg,
		category:    bybitDefaultCategory,
		accountType: bybitDefaultAccount,
	}, nil
}

func (c *bybitClient) Name() ExchangeName {
	return ExchangeBybit
}

func (c *bybitClient) Capabilities() Capabilities {
	return Capabilities{Spot: false, Derivatives: true, Streaming: true}
}

func (c *bybitClient) SubscribeCandles(ctx context.Context, symbol string, interval CandleInterval) (<-chan Candle, <-chan error) {
	if interval == CandleIntervalTick {
		return c.subscribeTicks(ctx, symbol)
	}
	dur, err := intervalDuration(interval)
	if err != nil {
		return channelWithError(err)
	}
	return subscribeByPolling(ctx, dur, func(ctx context.Context) (Candle, error) {
		end, err := lastClosedEnd(time.Now().UTC(), interval)
		if err != nil {
			return Candle{}, err
		}
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

func (c *bybitClient) GetCandles(ctx context.Context, symbol string, interval CandleInterval, start, end time.Time) ([]Candle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if interval == CandleIntervalTick {
		trades, err := c.getTickTrades(ctx, symbol, start, end)
		if err != nil {
			return nil, err
		}
		return filterClosedCandles(tickCandles(symbol, trades), end), nil
	}
	if interval == CandleIntervalSecond {
		return c.getSecondCandles(ctx, symbol, start, end)
	}
	bybitInterval, err := bybitInterval(interval)
	if err != nil {
		return nil, err
	}
	ranges, err := splitCandleRange(start, end, interval, 200)
	if err != nil {
		return nil, err
	}
	out := make([]Candle, 0)
	for _, r := range ranges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limit := estimateBybitLimit(r.Start, r.End, interval)
		params := map[string]interface{}{
			"category": c.category,
			"symbol":   symbol,
			"interval": bybitInterval,
		}
		if !r.Start.IsZero() {
			params["start"] = r.Start.UnixMilli()
		}
		if !r.End.IsZero() {
			params["end"] = r.End.UnixMilli()
		}
		if limit > 0 {
			params["limit"] = limit
		}

		resp, err := c.client.NewUtaBybitServiceWithParams(params).GetMarketKline(ctx)
		if err != nil {
			return nil, mapBybitError(err)
		}
		if err := bybitResponseError(resp); err != nil {
			return nil, mapBybitError(err)
		}
		klines, err := parseBybitKlines(resp)
		if err != nil {
			return nil, mapBybitError(err)
		}

		chunk := make([]Candle, 0, len(klines))
		for _, k := range klines {
			ms, err := strconv.ParseInt(k.StartTime, 10, 64)
			if err != nil {
				continue
			}
			openTime := time.UnixMilli(ms)
			chunk = append(chunk, Candle{
				Symbol:    symbol,
				Interval:  interval,
				OpenTime:  openTime,
				CloseTime: openTime.Add(mustDuration(interval)),
				Open:      k.OpenPrice,
				High:      k.HighPrice,
				Low:       k.LowPrice,
				Close:     k.ClosePrice,
				Volume:    k.Volume,
			})
		}
		if len(chunk) > 1 {
			sort.Slice(chunk, func(i, j int) bool {
				return chunk[i].OpenTime.Before(chunk[j].OpenTime)
			})
		}
		out = append(out, chunk...)
	}
	return filterClosedCandles(out, end), nil
}

func (c *bybitClient) getSecondCandles(ctx context.Context, symbol string, start, end time.Time) ([]Candle, error) {
	trades, err := c.getTickTrades(ctx, symbol, start, end)
	if err != nil {
		return nil, err
	}
	return filterClosedCandles(aggregateSecondCandles(symbol, trades), end), nil
}

func (c *bybitClient) getTickTrades(ctx context.Context, symbol string, start, end time.Time) ([]tradeTick, error) {
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
	params := map[string]interface{}{
		"category": c.category,
		"symbol":   symbol,
		"limit":    1000,
	}
	resp, err := c.client.NewUtaBybitServiceWithParams(params).GetPublicRecentTrades(ctx)
	if err != nil {
		return nil, mapBybitError(err)
	}
	if err := bybitResponseError(resp); err != nil {
		return nil, mapBybitError(err)
	}

	var result models.PublicRecentTradeHistory
	if err := decodeBybitResult(resp, &result); err != nil {
		return nil, mapBybitError(err)
	}

	trades := make([]tradeTick, 0, len(result.List))
	for _, t := range result.List {
		ts, err := parseBybitTradeTime(t.Time)
		if err != nil {
			continue
		}
		if ts.Before(start) || ts.After(end) {
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
			Time:  ts,
			Price: price,
			Size:  size,
		})
	}
	return trades, nil
}

func (c *bybitClient) subscribeTicks(ctx context.Context, symbol string) (<-chan Candle, <-chan error) {
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

func (c *bybitClient) GetBalances(ctx context.Context) ([]Balance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := map[string]interface{}{"accountType": c.accountType}
	resp, err := c.client.NewUtaBybitServiceWithParams(params).GetAccountWallet(ctx)
	if err != nil {
		return nil, mapBybitError(err)
	}
	if err := bybitResponseError(resp); err != nil {
		return nil, mapBybitError(err)
	}

	var payload struct {
		List []struct {
			AccountType string            `json:"accountType"`
			Coins       []models.CoinInfo `json:"coin"`
		} `json:"list"`
	}
	if err := decodeBybitResult(resp, &payload); err != nil {
		return nil, mapBybitError(err)
	}

	out := make([]Balance, 0)
	for _, account := range payload.List {
		_ = account
		for _, coin := range account.Coins {
			free := coin.Free
			if free == "" {
				free = coin.AvailableToWithdraw
			}
			locked := coin.Locked
			total := coin.WalletBalance
			if locked == "" && total != "" && free != "" {
				if tf, err := parseFloat(total); err == nil {
					if ff, err := parseFloat(free); err == nil && tf >= ff {
						locked = formatFloat(tf - ff)
					}
				}
			}
			if total == "" {
				total = sumStrings(free, locked)
			}
			out = append(out, Balance{
				Asset:  coin.Coin,
				Free:   free,
				Locked: locked,
				Total:  total,
			})
		}
	}
	return out, nil
}

func (c *bybitClient) ListOpenOrders(ctx context.Context, symbol string) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := map[string]interface{}{"category": c.category}
	if symbol != "" {
		params["symbol"] = symbol
	}
	resp, err := c.client.NewUtaBybitServiceWithParams(params).GetOpenOrders(ctx)
	if err != nil {
		return nil, mapBybitError(err)
	}
	if err := bybitResponseError(resp); err != nil {
		return nil, mapBybitError(err)
	}

	var result models.OpenOrdersInfo
	if err := decodeBybitResult(resp, &result); err != nil {
		return nil, mapBybitError(err)
	}

	out := make([]Order, 0, len(result.List))
	for _, o := range result.List {
		out = append(out, mapBybitOrderInfo(o))
	}
	return out, nil
}

func (c *bybitClient) ListOrders(ctx context.Context, symbol string, status OrderStatus) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if symbol == "" {
		return nil, ErrInvalidConfig
	}
	params := map[string]interface{}{"category": c.category}
	params["symbol"] = symbol
	resp, err := c.client.NewUtaBybitServiceWithParams(params).GetOrderHistory(ctx)
	if err != nil {
		return nil, mapBybitError(err)
	}
	if err := bybitResponseError(resp); err != nil {
		return nil, mapBybitError(err)
	}

	var result struct {
		List []models.OrderInfo `json:"list"`
	}
	if err := decodeBybitResult(resp, &result); err != nil {
		return nil, mapBybitError(err)
	}

	out := make([]Order, 0, len(result.List))
	for _, o := range result.List {
		mapped := mapBybitOrderInfo(o)
		if status != "" && mapped.Status != status {
			continue
		}
		out = append(out, mapped)
	}
	return out, nil
}

func (c *bybitClient) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	if req.Symbol == "" || req.Quantity == "" {
		return Order{}, ErrInvalidConfig
	}
	if req.Type == OrderTypeLimit && req.Price == "" {
		return Order{}, ErrInvalidConfig
	}
	order := c.client.NewPlaceOrderService(
		c.category,
		req.Symbol,
		mapBybitSide(req.Side),
		mapBybitType(req.Type),
		req.Quantity,
	)
	if req.Price != "" {
		order.Price(req.Price)
	}
	if req.TimeInForce != "" {
		order.TimeInForce(mapBybitTimeInForce(req.TimeInForce))
	}
	if req.ClientOrderID != "" {
		order.OrderLinkId(req.ClientOrderID)
	}
	if req.ReduceOnly {
		order.ReduceOnly(true)
	}
	resp, err := order.Do(ctx)
	if err != nil {
		return Order{}, mapBybitError(err)
	}
	if err := bybitResponseError(resp); err != nil {
		return Order{}, mapBybitError(err)
	}

	var result models.OrderResult
	if err := decodeBybitResult(resp, &result); err != nil {
		return Order{}, mapBybitError(err)
	}

	return Order{
		ID:        result.OrderId,
		Symbol:    req.Symbol,
		Side:      req.Side,
		Type:      req.Type,
		Status:    OrderStatusNew,
		Quantity:  req.Quantity,
		Price:     req.Price,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}, nil
}

func (c *bybitClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if symbol == "" {
		return ErrInvalidConfig
	}
	params := map[string]interface{}{
		"category": c.category,
		"symbol":   symbol,
		"orderId":  orderID,
	}
	resp, err := c.client.NewUtaBybitServiceWithParams(params).CancelOrder(ctx)
	if err != nil {
		return mapBybitError(err)
	}
	if err := bybitResponseError(resp); err != nil {
		return mapBybitError(err)
	}
	return nil
}

func (c *bybitClient) GetOrder(ctx context.Context, symbol, orderID string) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	if symbol == "" {
		return Order{}, ErrInvalidConfig
	}
	params := map[string]interface{}{
		"category": c.category,
		"symbol":   symbol,
		"orderId":  orderID,
	}
	resp, err := c.client.NewUtaBybitServiceWithParams(params).GetOrderHistory(ctx)
	if err != nil {
		return Order{}, mapBybitError(err)
	}
	if err := bybitResponseError(resp); err != nil {
		return Order{}, mapBybitError(err)
	}

	var result struct {
		List []models.OrderInfo `json:"list"`
	}
	if err := decodeBybitResult(resp, &result); err != nil {
		return Order{}, mapBybitError(err)
	}
	if len(result.List) == 0 {
		return Order{}, ErrOrderNotFound
	}
	return mapBybitOrderInfo(result.List[0]), nil
}

func (c *bybitClient) Ping(ctx context.Context) error {
	_, err := c.ServerTime(ctx)
	return err
}

func (c *bybitClient) ServerTime(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	resp, err := c.client.NewUtaBybitServiceNoParams().GetServerTime(ctx)
	if err != nil {
		return time.Time{}, mapBybitError(err)
	}
	if err := bybitResponseError(resp); err != nil {
		return time.Time{}, mapBybitError(err)
	}

	var result models.GetServerTimeResponse
	if err := decodeBybitFull(resp, &result); err != nil {
		return time.Time{}, mapBybitError(err)
	}
	sec, err := strconv.ParseInt(result.Result.TimeSecond, 10, 64)
	if err == nil && sec > 0 {
		return time.Unix(sec, 0), nil
	}
	nano, err := strconv.ParseInt(result.Result.TimeNano, 10, 64)
	if err == nil && nano > 0 {
		return time.Unix(0, nano), nil
	}
	return time.Time{}, errors.New("bybit: invalid server time")
}

func bybitInterval(interval CandleInterval) (string, error) {
	switch interval {
	case CandleIntervalMinute:
		return "1", nil
	case CandleIntervalHour:
		return "60", nil
	case CandleIntervalDay:
		return "D", nil
	default:
		return "", ErrNotSupported
	}
}

func mapBybitOrderInfo(o models.OrderInfo) Order {
	createdAt := parseBybitTime(o.CreatedTime)
	updatedAt := parseBybitTime(o.UpdatedTime)
	avgPrice := o.AvgPrice
	if avgPrice == "" && o.CumExecQty != "" && o.CumExecValue != "" {
		if qty, err := parseFloat(o.CumExecQty); err == nil && qty > 0 {
			if quote, err := parseFloat(o.CumExecValue); err == nil {
				avgPrice = formatFloat(quote / qty)
			}
		}
	}
	return Order{
		ID:        o.OrderId,
		Symbol:    o.Symbol,
		Side:      mapBybitSideFromAPI(o.Side),
		Type:      mapBybitTypeFromAPI(o.OrderType),
		Status:    mapBybitStatus(o.OrderStatus),
		Quantity:  o.Qty,
		Filled:    o.CumExecQty,
		Price:     o.Price,
		AvgPrice:  avgPrice,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

func mapBybitStatus(status string) OrderStatus {
	switch strings.ToUpper(status) {
	case "CREATED", "NEW":
		return OrderStatusNew
	case "PARTIALLYFILLED", "PARTIALLY_FILLED":
		return OrderStatusPartiallyFilled
	case "FILLED":
		return OrderStatusFilled
	case "CANCELLED", "CANCELED":
		return OrderStatusCanceled
	case "REJECTED":
		return OrderStatusRejected
	default:
		return OrderStatusRejected
	}
}

func mapBybitSide(side OrderSide) string {
	if side == OrderSideSell {
		return "Sell"
	}
	return "Buy"
}

func mapBybitSideFromAPI(side string) OrderSide {
	if strings.ToUpper(side) == "SELL" {
		return OrderSideSell
	}
	return OrderSideBuy
}

func mapBybitType(orderType OrderType) string {
	if orderType == OrderTypeMarket {
		return "Market"
	}
	return "Limit"
}

func mapBybitTypeFromAPI(orderType string) OrderType {
	if strings.ToUpper(orderType) == "MARKET" {
		return OrderTypeMarket
	}
	return OrderTypeLimit
}

func mapBybitTimeInForce(tif TimeInForce) string {
	switch tif {
	case TimeInForceIOC:
		return "IOC"
	case TimeInForceFOK:
		return "FOK"
	default:
		return "GTC"
	}
}

func mapBybitError(err error) error {
	return mapCommonError(err)
}

func bybitResponseError(resp *bybit.ServerResponse) error {
	if resp == nil {
		return errors.New("bybit: empty response")
	}
	if resp.RetCode != 0 {
		if resp.RetMsg == "" {
			return errors.New("bybit: request failed")
		}
		return errors.New("bybit: " + resp.RetMsg)
	}
	return nil
}

func decodeBybitResult(resp *bybit.ServerResponse, out interface{}) error {
	if resp == nil {
		return errors.New("bybit: empty response")
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func decodeBybitFull(resp *bybit.ServerResponse, out interface{}) error {
	if resp == nil {
		return errors.New("bybit: empty response")
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func parseBybitKlines(resp *bybit.ServerResponse) ([]*models.MarketKlineCandle, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	res, _, err := bybit.GetMarketKlineResponse(nil, data, nil)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("bybit: empty kline response")
	}
	return res.List, nil
}

func parseBybitTime(val string) time.Time {
	val = strings.TrimSpace(val)
	if val == "" {
		return time.Time{}
	}
	if ms, err := strconv.ParseInt(val, 10, 64); err == nil {
		if ms > 1e12 {
			return time.UnixMilli(ms)
		}
		return time.Unix(ms, 0)
	}
	return time.Time{}
}

func parseBybitTradeTime(val string) (time.Time, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return time.Time{}, errors.New("bybit: empty trade time")
	}
	ms, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	if ms > 1e12 {
		return time.UnixMilli(ms), nil
	}
	return time.Unix(ms, 0), nil
}

func estimateBybitLimit(start, end time.Time, interval CandleInterval) int {
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
	if count > 200 {
		return 200
	}
	return count
}

func mustDuration(interval CandleInterval) time.Duration {
	dur, err := intervalDuration(interval)
	if err != nil {
		return 0
	}
	return dur
}
