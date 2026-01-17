package broker

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/frankrap/bybit-api/rest"
)

const bybitMainnetURL = "https://api.bybit.com/"

type bybitClient struct {
	client *rest.ByBit
	cfg    Config
}

func newBybit(cfg Config) (*bybitClient, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = bybitMainnetURL
	}
	httpClient := &http.Client{Timeout: cfg.Timeout}
	client := rest.New(httpClient, baseURL, cfg.APIKey, cfg.APISecret, false)
	return &bybitClient{client: client, cfg: cfg}, nil
}

func (c *bybitClient) Name() ExchangeName {
	return ExchangeBybit
}

func (c *bybitClient) Capabilities() Capabilities {
	return Capabilities{Spot: false, Derivatives: true, Streaming: true}
}

func (c *bybitClient) SubscribeCandles(ctx context.Context, symbol string, interval CandleInterval) (<-chan Candle, <-chan error) {
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

func (c *bybitClient) GetCandles(ctx context.Context, symbol string, interval CandleInterval, start, end time.Time) ([]Candle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bybitInterval, err := bybitInterval(interval)
	if err != nil {
		return nil, err
	}
	limit := estimateBybitLimit(start, end, interval)
	if limit <= 0 {
		limit = 200
	}
	from := start.Unix()
	_, _, klines, err := c.client.LinearGetKLine(symbol, bybitInterval, from, limit)
	if err != nil {
		return nil, mapBybitError(err)
	}
	out := make([]Candle, 0, len(klines))
	for _, k := range klines {
		out = append(out, Candle{
			Symbol:    symbol,
			Interval:  interval,
			OpenTime:  time.Unix(k.OpenTime, 0),
			CloseTime: time.Unix(k.OpenTime, 0).Add(mustDuration(interval)),
			Open:      formatFloat(k.Open),
			High:      formatFloat(k.High),
			Low:       formatFloat(k.Low),
			Close:     formatFloat(k.Close),
			Volume:    formatFloat(k.Volume),
		})
	}
	return out, nil
}

func (c *bybitClient) GetBalances(ctx context.Context) ([]Balance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	coins := []string{"BTC", "ETH", "EOS", "XRP", "USDT"}
	out := make([]Balance, 0, len(coins))
	for _, coin := range coins {
		_, _, bal, err := c.client.GetWalletBalance(coin)
		if err != nil {
			return nil, mapBybitError(err)
		}
		locked := bal.WalletBalance - bal.AvailableBalance
		out = append(out, Balance{
			Asset:  coin,
			Free:   formatFloat(bal.AvailableBalance),
			Locked: formatFloat(locked),
			Total:  formatFloat(bal.WalletBalance),
		})
	}
	return out, nil
}

func (c *bybitClient) ListOpenOrders(ctx context.Context, symbol string) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if symbol == "" {
		return nil, ErrNotSupported
	}
	_, _, orders, err := c.client.LinearGetActiveOrders(symbol)
	if err != nil {
		return nil, mapBybitError(err)
	}
	out := make([]Order, 0, len(orders.Result))
	for _, o := range orders.Result {
		out = append(out, mapBybitOrder(o))
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
	bybitSide := mapBybitSide(req.Side)
	bybitType := mapBybitType(req.Type)
	quantity := mustFloat(req.Quantity)
	price := mustFloat(req.Price)
	timeInForce := mapBybitTimeInForce(req.TimeInForce)
	_, _, order, err := c.client.LinearCreateOrder(bybitSide, bybitType, price, quantity, timeInForce, 0, 0, req.ReduceOnly, false, req.ClientOrderID, req.Symbol)
	if err != nil {
		return Order{}, mapBybitError(err)
	}
	return mapBybitOrder(order), nil
}

func (c *bybitClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, _, err := c.client.LinearCancelOrder(orderID, "", symbol)
	return mapBybitError(err)
}

func (c *bybitClient) GetOrder(ctx context.Context, symbol, orderID string) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	_, _, orderResp, err := c.client.LinearGetActiveOrder(symbol, orderID, "")
	if err != nil {
		return Order{}, mapBybitError(err)
	}
	return mapBybitOrder(orderResp.Result), nil
}

func (c *bybitClient) Ping(ctx context.Context) error {
	_, err := c.ServerTime(ctx)
	return err
}

func (c *bybitClient) ServerTime(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	_, _, ms, err := c.client.GetServerTime()
	if err != nil {
		return time.Time{}, mapBybitError(err)
	}
	return time.UnixMilli(ms), nil
}

func bybitInterval(interval CandleInterval) (string, error) {
	switch interval {
	case CandleIntervalTick, CandleIntervalMinute:
		return "1", nil
	case CandleIntervalHour:
		return "60", nil
	case CandleIntervalDay:
		return "D", nil
	default:
		return "", ErrNotSupported
	}
}

func mapBybitOrder(o rest.Order) Order {
	avgPrice := ""
	if o.CumExecQty.String() != "" && o.CumExecValue.String() != "" {
		qty, err := parseFloat(o.CumExecQty.String())
		if err == nil && qty > 0 {
			if quote, err := parseFloat(o.CumExecValue.String()); err == nil {
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
		Quantity:  o.Qty.String(),
		Filled:    o.CumExecQty.String(),
		Price:     o.Price.String(),
		AvgPrice:  avgPrice,
		CreatedAt: o.CreatedAt,
		UpdatedAt: o.UpdatedAt,
	}
}

func mapBybitStatus(status string) OrderStatus {
	switch strings.ToUpper(status) {
	case "NEW", "CREATED":
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
		return "ImmediateOrCancel"
	case TimeInForceFOK:
		return "FillOrKill"
	default:
		return "GoodTillCancel"
	}
}

func mapBybitError(err error) error {
	return mapCommonError(err)
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

func mustFloat(val string) float64 {
	f, _ := parseFloat(val)
	return f
}
