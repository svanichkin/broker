package broker

import (
	"bytes"
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
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	bybitMainnetURL      = "https://api.bybit.com"
	bybitDefaultCategory = "linear"
	bybitDefaultAccount  = "UNIFIED"
	bybitDefaultRecvWin  = "5000"
)

type bybitClient struct {
	httpClient  *http.Client
	cfg         Config
	category    string
	accountType string
	baseURL     string
	recvWindow  string
}

func newBybit(cfg Config) (*bybitClient, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = bybitMainnetURL
	}
	client := &http.Client{Timeout: cfg.Timeout}
	return &bybitClient{
		httpClient:  client,
		cfg:         cfg,
		category:    bybitDefaultCategory,
		accountType: bybitDefaultAccount,
		baseURL:     strings.TrimRight(baseURL, "/"),
		recvWindow:  bybitDefaultRecvWin,
	}, nil
}

func (c *bybitClient) Name() ExchangeName {
	return ExchangeBybit
}

func (c *bybitClient) Capabilities() Capabilities {
	return Capabilities{Spot: true, Derivatives: true, Streaming: true}
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
		params := map[string]string{
			"category": c.category,
			"symbol":   symbol,
			"interval": bybitInterval,
		}
		if !r.Start.IsZero() {
			params["start"] = strconv.FormatInt(r.Start.UnixMilli(), 10)
		}
		if !r.End.IsZero() {
			params["end"] = strconv.FormatInt(r.End.UnixMilli(), 10)
		}
		if limit > 0 {
			params["limit"] = strconv.Itoa(limit)
		}

		resp, err := c.doRequest(ctx, http.MethodGet, "/v5/market/kline", params, nil, false)
		if err != nil {
			return nil, err
		}
		var result bybitV5KlineResult
		if err := parseBybitResult(resp, &result); err != nil {
			return nil, err
		}

		chunk := make([]Candle, 0, len(result.List))
		for _, k := range result.List {
			if len(k) < 6 {
				continue
			}
			ms, err := strconv.ParseInt(k[0], 10, 64)
			if err != nil {
				continue
			}
			openTime := time.UnixMilli(ms)
			chunk = append(chunk, Candle{
				Symbol:    symbol,
				Interval:  interval,
				OpenTime:  openTime,
				CloseTime: openTime.Add(mustDuration(interval)),
				Open:      k[1],
				High:      k[2],
				Low:       k[3],
				Close:     k[4],
				Volume:    k[5],
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
	params := map[string]string{
		"category": c.category,
		"symbol":   symbol,
		"limit":    "1000",
	}
	resp, err := c.doRequest(ctx, http.MethodGet, "/v5/market/recent-trade", params, nil, false)
	if err != nil {
		return nil, err
	}
	var result bybitV5RecentTrades
	if err := parseBybitResult(resp, &result); err != nil {
		return nil, err
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
	params := map[string]string{"accountType": c.accountType}
	resp, err := c.doRequest(ctx, http.MethodGet, "/v5/account/wallet-balance", params, nil, true)
	if err != nil {
		return nil, err
	}
	var payload bybitV5WalletBalance
	if err := parseBybitResult(resp, &payload); err != nil {
		return nil, err
	}

	out := make([]Balance, 0)
	for _, account := range payload.List {
		for _, coin := range account.Coin {
			free := coin.AvailableToWithdraw
			if free == "" {
				free = coin.WalletBalance
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

func (c *bybitClient) GetFeeRates(ctx context.Context, symbol string, market MarketType) (FeeRates, error) {
	if err := ctx.Err(); err != nil {
		return FeeRates{}, err
	}
	category := bybitCategory(market, c.category)
	params := map[string]string{"category": category}
	if symbol != "" {
		params["symbol"] = symbol
	}
	resp, err := c.doRequest(ctx, http.MethodGet, "/v5/account/fee-rate", params, nil, true)
	if err != nil {
		return FeeRates{}, err
	}
	var payload bybitV5FeeRates
	if err := parseBybitResult(resp, &payload); err != nil {
		return FeeRates{}, err
	}
	if len(payload.List) == 0 {
		return FeeRates{}, ErrNotSupported
	}
	if symbol != "" {
		for _, item := range payload.List {
			if strings.EqualFold(item.Symbol, symbol) {
				return FeeRates{Maker: item.MakerFeeRate, Taker: item.TakerFeeRate}, nil
			}
		}
	}
	item := payload.List[0]
	return FeeRates{Maker: item.MakerFeeRate, Taker: item.TakerFeeRate}, nil
}

func (c *bybitClient) ListOpenOrders(ctx context.Context, symbol string) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.listOrdersAllCategories(ctx, "/v5/order/realtime", symbol)
}

func (c *bybitClient) ListOrders(ctx context.Context, symbol string, status OrderStatus) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if symbol == "" {
		return nil, ErrInvalidConfig
	}
	orders, err := c.listOrdersAllCategories(ctx, "/v5/order/history", symbol)
	if err != nil {
		return nil, err
	}
	if status == "" {
		return orders, nil
	}
	filtered := make([]Order, 0, len(orders))
	for _, o := range orders {
		if o.Status != status {
			continue
		}
		filtered = append(filtered, o)
	}
	return filtered, nil
}

func (c *bybitClient) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	if req.Symbol == "" || req.Quantity == "" {
		return Order{}, ErrInvalidConfig
	}
	category := bybitCategory(req.Market, c.category)
	if req.Leverage != "" {
		if category == "spot" {
			return Order{}, ErrNotSupported
		}
		payload := map[string]string{
			"category":     category,
			"symbol":       req.Symbol,
			"buyLeverage":  req.Leverage,
			"sellLeverage": req.Leverage,
		}
		if _, err := c.doRequest(ctx, http.MethodPost, "/v5/position/set-leverage", nil, payload, true); err != nil {
			return Order{}, err
		}
	}
	if req.Type == OrderTypeLimit && req.Price == "" {
		return Order{}, ErrInvalidConfig
	}
	payload := map[string]interface{}{
		"category":  category,
		"symbol":    req.Symbol,
		"side":      mapBybitSide(req.Side),
		"orderType": mapBybitType(req.Type),
		"qty":       req.Quantity,
	}
	if req.Price != "" {
		payload["price"] = req.Price
	}
	if req.TimeInForce != "" {
		payload["timeInForce"] = mapBybitTimeInForce(req.TimeInForce)
	}
	if req.ClientOrderID != "" {
		payload["orderLinkId"] = req.ClientOrderID
	}
	if req.ReduceOnly {
		payload["reduceOnly"] = true
	}
	if req.PositionIdx != "" {
		payload["positionIdx"] = req.PositionIdx
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/v5/order/create", nil, payload, true)
	if err != nil {
		return Order{}, err
	}
	var result bybitV5OrderCreate
	if err := parseBybitResult(resp, &result); err != nil {
		return Order{}, err
	}

	return Order{
		ID:         result.OrderId,
		Symbol:     req.Symbol,
		Market:     req.Market,
		Side:       req.Side,
		Type:       req.Type,
		Status:     OrderStatusNew,
		Quantity:   req.Quantity,
		Price:      req.Price,
		ReduceOnly: req.ReduceOnly,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}, nil
}

func (c *bybitClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if symbol == "" {
		return ErrInvalidConfig
	}
	categories := []string{c.category, "spot"}
	for _, category := range categories {
		payload := map[string]string{
			"category": category,
			"symbol":   symbol,
			"orderId":  orderID,
		}
		if _, err := c.doRequest(ctx, http.MethodPost, "/v5/order/cancel", nil, payload, true); err != nil {
			mapped := mapBybitError(err)
			if errors.Is(mapped, ErrOrderNotFound) {
				continue
			}
			return mapped
		}
		return nil
	}
	return ErrOrderNotFound
}

func (c *bybitClient) GetOrder(ctx context.Context, symbol, orderID string) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	if symbol == "" {
		return Order{}, ErrInvalidConfig
	}
	categories := []string{c.category, "spot"}
	paths := []string{"/v5/order/realtime", "/v5/order/history"}
	for _, category := range categories {
		for _, path := range paths {
			params := map[string]string{
				"category": category,
				"symbol":   symbol,
				"orderId":  orderID,
				"limit":    "1",
			}
			resp, err := c.doRequest(ctx, http.MethodGet, path, params, nil, true)
			if err != nil {
				mapped := mapBybitError(err)
				if errors.Is(mapped, ErrOrderNotFound) {
					continue
				}
				return Order{}, mapped
			}
			var result bybitV5OrderListResult
			if err := parseBybitResult(resp, &result); err != nil {
				return Order{}, err
			}
			if len(result.List) == 0 {
				continue
			}
			return mapBybitOrderInfo(result.List[0], category), nil
		}
	}
	return Order{}, ErrOrderNotFound
}

func (c *bybitClient) Ping(ctx context.Context) error {
	_, err := c.ServerTime(ctx)
	return err
}

func (c *bybitClient) ServerTime(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	resp, err := c.doRequest(ctx, http.MethodGet, "/v5/market/time", nil, nil, false)
	if err != nil {
		return time.Time{}, err
	}
	var result bybitV5ServerTime
	if err := parseBybitResult(resp, &result); err != nil {
		return time.Time{}, err
	}
	if result.TimeSecond != "" {
		sec, err := strconv.ParseInt(result.TimeSecond, 10, 64)
		if err == nil && sec > 0 {
			return time.Unix(sec, 0), nil
		}
	}
	if result.TimeNano != "" {
		nano, err := strconv.ParseInt(result.TimeNano, 10, 64)
		if err == nil && nano > 0 {
			return time.Unix(0, nano), nil
		}
	}
	return time.Time{}, errors.New("bybit: invalid server time")
}

func (c *bybitClient) listOrdersPaged(ctx context.Context, path string, category string, symbol string) ([]Order, error) {
	out := make([]Order, 0)
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		params := map[string]string{
			"category": category,
			"limit":    "200",
		}
		if symbol != "" {
			params["symbol"] = symbol
		}
		if cursor != "" {
			params["cursor"] = cursor
		} else {
			delete(params, "cursor")
		}
		resp, err := c.doRequest(ctx, http.MethodGet, path, params, nil, true)
		if err != nil {
			return nil, err
		}
		var result bybitV5OrderListResult
		if err := parseBybitResult(resp, &result); err != nil {
			return nil, err
		}
		for _, o := range result.List {
			out = append(out, mapBybitOrderInfo(o, category))
		}
		if result.NextPageCursor == "" || result.NextPageCursor == cursor {
			break
		}
		cursor = result.NextPageCursor
	}
	return out, nil
}

func (c *bybitClient) listOrdersAllCategories(ctx context.Context, path string, symbol string) ([]Order, error) {
	orders := make([]Order, 0)
	categories := []string{c.category, "spot"}
	seen := make(map[string]struct{})
	for _, category := range categories {
		if category == "spot" && symbol == "" {
			continue
		}
		chunk, err := c.listOrdersPaged(ctx, path, category, symbol)
		if err != nil {
			return nil, err
		}
		for _, o := range chunk {
			if o.ID == "" {
				orders = append(orders, o)
				continue
			}
			key := category + ":" + o.ID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			orders = append(orders, o)
		}
	}
	return orders, nil
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

func mapBybitOrderInfo(o bybitV5Order, category string) Order {
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
		ID:             o.OrderId,
		Symbol:         o.Symbol,
		Market:         bybitMarketFromCategory(category),
		Side:           mapBybitSideFromAPI(o.Side),
		Type:           mapBybitTypeFromAPI(o.OrderType),
		Status:         mapBybitStatus(o.OrderStatus),
		Quantity:       o.Qty,
		Filled:         o.CumExecQty,
		Price:          o.Price,
		AvgPrice:       avgPrice,
		CumExecFee:     o.CumExecFee,
		ReduceOnly:     o.ReduceOnly,
		CloseOnTrigger: o.CloseOnTrigger,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
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
	case "TRIGGERED", "UNTRIGGERED", "DEACTIVATED":
		return OrderStatusNew
	case "CANCELLED", "CANCELED":
		return OrderStatusCanceled
	case "REJECTED":
		return OrderStatusRejected
	default:
		return OrderStatusUnknown
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

func bybitCategory(market MarketType, fallback string) string {
	switch market {
	case MarketSpot:
		return "spot"
	case MarketDerivatives:
		return fallback
	default:
		return fallback
	}
}

func bybitMarketFromCategory(category string) MarketType {
	if strings.EqualFold(category, "spot") {
		return MarketSpot
	}
	return MarketDerivatives
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
	if err == nil {
		return nil
	}
	var apiErr *bybitAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 10001, 10002, 110001, 110002:
			return wrapError(apiErr, ErrOrderNotFound)
		}
		msg := strings.ToLower(apiErr.Msg)
		if strings.Contains(msg, "category") && strings.Contains(msg, "invalid") {
			return wrapError(apiErr, ErrOrderNotFound)
		}
	}
	return mapCommonError(err)
}

type bybitV5Response struct {
	RetCode int             `json:"retCode"`
	RetMsg  string          `json:"retMsg"`
	Result  json.RawMessage `json:"result"`
	Time    int64           `json:"time"`
}

type bybitAPIError struct {
	Code int
	Msg  string
}

func (e *bybitAPIError) Error() string {
	return fmt.Sprintf("bybit: %d %s", e.Code, e.Msg)
}

type bybitV5KlineResult struct {
	List [][]string `json:"list"`
}

type bybitV5RecentTrades struct {
	List []struct {
		Time  string `json:"time"`
		Price string `json:"price"`
		Size  string `json:"size"`
	} `json:"list"`
}

type bybitV5WalletBalance struct {
	List []struct {
		AccountType string            `json:"accountType"`
		Coin        []bybitV5CoinInfo `json:"coin"`
	} `json:"list"`
}

type bybitV5CoinInfo struct {
	Coin                string `json:"coin"`
	AvailableToWithdraw string `json:"availableToWithdraw"`
	WalletBalance       string `json:"walletBalance"`
	Locked              string `json:"locked"`
}

type bybitV5FeeRates struct {
	List []struct {
		Symbol       string `json:"symbol"`
		MakerFeeRate string `json:"makerFeeRate"`
		TakerFeeRate string `json:"takerFeeRate"`
	} `json:"list"`
}

type bybitV5OrderListResult struct {
	List           []bybitV5Order `json:"list"`
	NextPageCursor string         `json:"nextPageCursor"`
}

type bybitV5Order struct {
	OrderId        string `json:"orderId"`
	OrderLinkId    string `json:"orderLinkId"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
	OrderType      string `json:"orderType"`
	OrderStatus    string `json:"orderStatus"`
	Qty            string `json:"qty"`
	Price          string `json:"price"`
	AvgPrice       string `json:"avgPrice"`
	CumExecQty     string `json:"cumExecQty"`
	CumExecValue   string `json:"cumExecValue"`
	CumExecFee     string `json:"cumExecFee"`
	ReduceOnly     bool   `json:"reduceOnly"`
	CloseOnTrigger bool   `json:"closeOnTrigger"`
	CreatedTime    string `json:"createdTime"`
	UpdatedTime    string `json:"updatedTime"`
}

type bybitV5OrderCreate struct {
	OrderId     string `json:"orderId"`
	OrderLinkId string `json:"orderLinkId"`
}

type bybitV5ServerTime struct {
	TimeSecond string `json:"timeSecond"`
	TimeNano   string `json:"timeNano"`
}

func (c *bybitClient) doRequest(ctx context.Context, method, path string, params map[string]string, body interface{}, auth bool) ([]byte, error) {
	query := url.Values{}
	for k, v := range params {
		if v == "" {
			continue
		}
		query.Set(k, v)
	}
	queryStr := query.Encode()
	urlStr := c.baseURL + path
	if queryStr != "" {
		urlStr += "?" + queryStr
	}

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	var payload string
	if method == http.MethodGet {
		payload = queryStr
	} else {
		payload = string(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		timestamp := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
		sign := bybitSign(c.cfg.APISecret, timestamp, c.cfg.APIKey, c.recvWindow, payload)
		req.Header.Set("X-BAPI-API-KEY", c.cfg.APIKey)
		req.Header.Set("X-BAPI-SIGN", sign)
		req.Header.Set("X-BAPI-SIGN-TYPE", "2")
		req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
		req.Header.Set("X-BAPI-RECV-WINDOW", c.recvWindow)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, mapBybitError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var envelope bybitV5Response
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.RetCode != 0 {
		return nil, mapBybitError(&bybitAPIError{Code: envelope.RetCode, Msg: envelope.RetMsg})
	}
	return data, nil
}

func parseBybitResult(data []byte, out interface{}) error {
	var envelope bybitV5Response
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Result == nil {
		return errors.New("bybit: empty result")
	}
	return json.Unmarshal(envelope.Result, out)
}

func bybitSign(secret, timestamp, apiKey, recvWindow, payload string) string {
	msg := timestamp + apiKey + recvWindow + payload
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
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
