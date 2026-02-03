package broker

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/common"
)

type binanceClient struct {
	client *binance.Client
	cfg    Config
}

func newBinance(cfg Config) (*binanceClient, error) {
	client := binance.NewClient(cfg.APIKey, cfg.APISecret)
	if cfg.BaseURL != "" {
		client.BaseURL = cfg.BaseURL
	}
	client.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	return &binanceClient{client: client, cfg: cfg}, nil
}

func (c *binanceClient) Name() ExchangeName {
	return ExchangeBinance
}

func (c *binanceClient) Capabilities() Capabilities {
	return Capabilities{Spot: true, Derivatives: false, Streaming: true}
}

func (c *binanceClient) SubscribeCandles(ctx context.Context, symbol string, interval CandleInterval) (<-chan Candle, <-chan error) {
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

func (c *binanceClient) GetCandles(ctx context.Context, symbol string, interval CandleInterval, start, end time.Time) ([]Candle, error) {
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
	binanceInterval, err := binanceInterval(interval)
	if err != nil {
		return nil, err
	}
	ranges, err := splitCandleRange(start, end, interval, 1000)
	if err != nil {
		return nil, err
	}
	out := make([]Candle, 0)
	for _, r := range ranges {
		service := c.client.NewKlinesService().Symbol(symbol).Interval(binanceInterval)
		if !r.Start.IsZero() {
			service.StartTime(r.Start.UnixMilli())
		}
		if !r.End.IsZero() {
			service.EndTime(r.End.UnixMilli())
		}
		limit := estimateLimit(r.Start, r.End, interval)
		if limit > 0 {
			service.Limit(limit)
		}
		klines, err := service.Do(ctx)
		if err != nil {
			return nil, mapBinanceError(err)
		}
		for _, k := range klines {
			out = append(out, Candle{
				Symbol:    symbol,
				Interval:  interval,
				OpenTime:  time.UnixMilli(k.OpenTime),
				CloseTime: time.UnixMilli(k.CloseTime),
				Open:      k.Open,
				High:      k.High,
				Low:       k.Low,
				Close:     k.Close,
				Volume:    k.Volume,
				Trades:    strconv.FormatInt(k.TradeNum, 10),
			})
		}
	}
	if len(ranges) > 1 && len(out) > 1 {
		sort.Slice(out, func(i, j int) bool {
			return out[i].OpenTime.Before(out[j].OpenTime)
		})
	}
	return filterClosedCandles(out, end), nil
}

func (c *binanceClient) getSecondCandles(ctx context.Context, symbol string, start, end time.Time) ([]Candle, error) {
	trades, err := c.getTickTrades(ctx, symbol, start, end)
	if err != nil {
		return nil, err
	}
	return filterClosedCandles(aggregateSecondCandles(symbol, trades), end), nil
}

func (c *binanceClient) getTickTrades(ctx context.Context, symbol string, start, end time.Time) ([]tradeTick, error) {
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
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	trades := make([]tradeTick, 0)

	for {
		service := c.client.NewAggTradesService().Symbol(symbol).StartTime(startMs).EndTime(endMs).Limit(1000)
		aggTrades, err := service.Do(ctx)
		if err != nil {
			return nil, mapBinanceError(err)
		}
		if len(aggTrades) == 0 {
			break
		}
		for _, t := range aggTrades {
			if t.Timestamp < startMs || t.Timestamp > endMs {
				continue
			}
			price, err := parseFloat(t.Price)
			if err != nil {
				continue
			}
			size, err := parseFloat(t.Quantity)
			if err != nil {
				continue
			}
			trades = append(trades, tradeTick{
				Time:  time.UnixMilli(t.Timestamp),
				Price: price,
				Size:  size,
			})
		}
		lastTs := aggTrades[len(aggTrades)-1].Timestamp
		if len(aggTrades) < 1000 || lastTs >= endMs || lastTs <= startMs {
			break
		}
		startMs = lastTs + 1
	}

	return trades, nil
}

func (c *binanceClient) subscribeTicks(ctx context.Context, symbol string) (<-chan Candle, <-chan error) {
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

func (c *binanceClient) GetBalances(ctx context.Context) ([]Balance, error) {
	account, err := c.client.NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, mapBinanceError(err)
	}
	out := make([]Balance, 0, len(account.Balances))
	for _, b := range account.Balances {
		total := sumStrings(b.Free, b.Locked)
		out = append(out, Balance{
			Asset:  b.Asset,
			Free:   b.Free,
			Locked: b.Locked,
			Total:  total,
		})
	}
	return out, nil
}

func (c *binanceClient) GetFeeRates(ctx context.Context, symbol string, market MarketType) (FeeRates, error) {
	if err := ctx.Err(); err != nil {
		return FeeRates{}, err
	}
	if market == MarketDerivatives {
		return FeeRates{}, ErrNotSupported
	}
	svc := c.client.NewTradeFeeService()
	if symbol != "" {
		svc.Symbol(strings.ToUpper(symbol))
	}
	fees, err := svc.Do(ctx)
	if err != nil {
		return FeeRates{}, mapBinanceError(err)
	}
	if len(fees) == 0 {
		return FeeRates{}, ErrNotSupported
	}
	fee := fees[0]
	return FeeRates{Maker: fee.MakerCommission, Taker: fee.TakerCommission}, nil
}

func (c *binanceClient) ListOpenOrders(ctx context.Context, symbol string) ([]Order, error) {
	service := c.client.NewListOpenOrdersService()
	if symbol != "" {
		service.Symbol(symbol)
	}
	orders, err := service.Do(ctx)
	if err != nil {
		return nil, mapBinanceError(err)
	}
	out := make([]Order, 0, len(orders))
	for _, o := range orders {
		out = append(out, mapBinanceOrder(o))
	}
	return out, nil
}

func (c *binanceClient) ListOrders(ctx context.Context, symbol string, status OrderStatus) ([]Order, error) {
	if symbol == "" {
		return nil, ErrInvalidConfig
	}
	service := c.client.NewListOrdersService().Symbol(symbol)
	orders, err := service.Do(ctx)
	if err != nil {
		return nil, mapBinanceError(err)
	}
	out := make([]Order, 0, len(orders))
	for _, o := range orders {
		mapped := mapBinanceOrder(o)
		if status != "" && mapped.Status != status {
			continue
		}
		out = append(out, mapped)
	}
	return out, nil
}

func (c *binanceClient) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error) {
	if req.Market == MarketDerivatives {
		return Order{}, ErrNotSupported
	}
	if req.Leverage != "" {
		return Order{}, ErrNotSupported
	}
	if req.Symbol == "" || req.Quantity == "" {
		return Order{}, ErrInvalidConfig
	}
	service := c.client.NewCreateOrderService().
		Symbol(req.Symbol).
		Side(binance.SideType(req.Side)).
		Type(binance.OrderType(req.Type)).
		Quantity(req.Quantity)

	if req.ClientOrderID != "" {
		service.NewClientOrderID(req.ClientOrderID)
	}
	if req.Type == OrderTypeLimit {
		if req.Price == "" {
			return Order{}, ErrInvalidConfig
		}
		service.Price(req.Price)
		tif := req.TimeInForce
		if tif == "" {
			tif = TimeInForceGTC
		}
		service.TimeInForce(binance.TimeInForceType(tif))
	}
	resp, err := service.Do(ctx)
	if err != nil {
		return Order{}, mapBinanceError(err)
	}
	return mapBinanceCreateOrder(resp), nil
}

func (c *binanceClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	if symbol == "" {
		return ErrInvalidConfig
	}
	service := c.client.NewCancelOrderService().Symbol(symbol)
	if id, err := strconv.ParseInt(orderID, 10, 64); err == nil {
		service.OrderID(id)
	} else {
		service.OrigClientOrderID(orderID)
	}
	_, err := service.Do(ctx)
	return mapBinanceError(err)
}

func (c *binanceClient) GetOrder(ctx context.Context, symbol, orderID string) (Order, error) {
	if symbol == "" {
		return Order{}, ErrInvalidConfig
	}
	service := c.client.NewGetOrderService().Symbol(symbol)
	if id, err := strconv.ParseInt(orderID, 10, 64); err == nil {
		service.OrderID(id)
	} else {
		service.OrigClientOrderID(orderID)
	}
	order, err := service.Do(ctx)
	if err != nil {
		return Order{}, mapBinanceError(err)
	}
	return mapBinanceOrder(order), nil
}

func (c *binanceClient) Ping(ctx context.Context) error {
	return mapBinanceError(c.client.NewPingService().Do(ctx))
}

func (c *binanceClient) ServerTime(ctx context.Context) (time.Time, error) {
	ms, err := c.client.NewServerTimeService().Do(ctx)
	if err != nil {
		return time.Time{}, mapBinanceError(err)
	}
	return time.UnixMilli(ms), nil
}

func binanceInterval(interval CandleInterval) (string, error) {
	switch interval {
	case CandleIntervalMinute:
		return "1m", nil
	case CandleIntervalHour:
		return "1h", nil
	case CandleIntervalDay:
		return "1d", nil
	default:
		return "", ErrNotSupported
	}
}

func mapBinanceOrder(o *binance.Order) Order {
	avgPrice := ""
	if o.ExecutedQuantity != "" && o.CummulativeQuoteQuantity != "" {
		if qty, err := parseFloat(o.ExecutedQuantity); err == nil && qty > 0 {
			if quote, err := parseFloat(o.CummulativeQuoteQuantity); err == nil {
				avgPrice = formatFloat(quote / qty)
			}
		}
	}
	return Order{
		ID:        strconv.FormatInt(o.OrderID, 10),
		Symbol:    o.Symbol,
		Market:    MarketSpot,
		Side:      OrderSide(o.Side),
		Type:      OrderType(o.Type),
		Status:    mapBinanceStatus(o.Status),
		Quantity:  o.OrigQuantity,
		Filled:    o.ExecutedQuantity,
		Price:     o.Price,
		AvgPrice:  avgPrice,
		CreatedAt: time.UnixMilli(o.Time),
		UpdatedAt: time.UnixMilli(o.UpdateTime),
	}
}

func mapBinanceCreateOrder(o *binance.CreateOrderResponse) Order {
	avgPrice := ""
	if o.ExecutedQuantity != "" && o.CummulativeQuoteQuantity != "" {
		if qty, err := parseFloat(o.ExecutedQuantity); err == nil && qty > 0 {
			if quote, err := parseFloat(o.CummulativeQuoteQuantity); err == nil {
				avgPrice = formatFloat(quote / qty)
			}
		}
	}
	return Order{
		ID:        strconv.FormatInt(o.OrderID, 10),
		Symbol:    o.Symbol,
		Market:    MarketSpot,
		Side:      OrderSide(o.Side),
		Type:      OrderType(o.Type),
		Status:    mapBinanceStatus(o.Status),
		Quantity:  o.OrigQuantity,
		Filled:    o.ExecutedQuantity,
		Price:     o.Price,
		AvgPrice:  avgPrice,
		CreatedAt: time.UnixMilli(o.TransactTime),
		UpdatedAt: time.UnixMilli(o.TransactTime),
	}
}

func mapBinanceStatus(status binance.OrderStatusType) OrderStatus {
	switch status {
	case binance.OrderStatusTypeNew:
		return OrderStatusNew
	case binance.OrderStatusTypePartiallyFilled:
		return OrderStatusPartiallyFilled
	case binance.OrderStatusTypeFilled:
		return OrderStatusFilled
	case binance.OrderStatusTypeCanceled:
		return OrderStatusCanceled
	case binance.OrderStatusTypeRejected:
		return OrderStatusRejected
	case binance.OrderStatusTypeExpired, binance.OrderStatusTypePendingCancel:
		return OrderStatusCanceled
	default:
		return OrderStatusRejected
	}
}

func mapBinanceError(err error) error {
	if err == nil {
		return nil
	}
	if apiErr, ok := err.(*common.APIError); ok {
		switch apiErr.Code {
		case -2015, -2014:
			return wrapError(err, ErrAuth)
		case -2013:
			return wrapError(err, ErrOrderNotFound)
		case -2010:
			if strings.Contains(strings.ToLower(apiErr.Message), "insufficient") {
				return wrapError(err, ErrInsufficientBalance)
			}
		case -1003:
			return wrapError(err, ErrRateLimited)
		}
	}
	return mapCommonError(err)
}

func estimateLimit(start, end time.Time, interval CandleInterval) int {
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
	if count > 1000 {
		return 1000
	}
	return count
}

func sumStrings(a, b string) string {
	fa, err := parseFloat(a)
	if err != nil {
		return ""
	}
	fb, err := parseFloat(b)
	if err != nil {
		return ""
	}
	return formatFloat(fa + fb)
}
