# broker

Unified Go facade over Binance, Bybit, and dYdX.

Goals:
- One interface for balances, orders, candles, ping, and server time.
- Unified domain models and error types.
- Same method names and signatures across exchanges.

## Install

```bash
go get github.com/svanichkin/broker
```

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/svanichkin/broker"
)

func main() {
	ctx := context.Background()
	ex, err := broker.NewExchange(ctx, broker.Config{
		Exchange:  broker.ExchangeBinance,
		APIKey:    "YOUR_KEY",
		APISecret: "YOUR_SECRET",
		Timeout:   10 * time.Second,
	})
	if err != nil {
		panic(err)
	}

	if err := ex.Ping(ctx); err != nil {
		panic(err)
	}

	now, err := ex.ServerTime(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println("server time:", now)
}
```

## Config

```go
cfg := broker.Config{
	Exchange:  broker.ExchangeBybit, // or broker.ExchangeBinance / broker.ExchangeDydx
	APIKey:    "YOUR_KEY",
	APISecret: "YOUR_SECRET",
	Passphrase: "YOUR_PASSPHRASE", // required for dYdX
	BaseURL:   "",                // optional override
	Timeout:   10 * time.Second,

	// dYdX-only fields
	EthereumAddress:          "0x...",
	StarkPublicKey:           "...",
	StarkPrivateKey:          "...",
	StarkPublicKeyYCoordinate: "...",
}
```

Create facade or exchange:

```go
fx, err := broker.New(ctx, cfg)      // returns *broker.Facade
ex, err := broker.NewExchange(ctx, cfg) // returns broker.Exchange
```

## Exchange interface

```go
type Exchange interface {
	Name() ExchangeName
	Capabilities() Capabilities

	SubscribeCandles(ctx context.Context, symbol string, interval CandleInterval) (<-chan Candle, <-chan error)
	GetCandles(ctx context.Context, symbol string, interval CandleInterval, start, end time.Time) ([]Candle, error)

	GetBalances(ctx context.Context) ([]Balance, error)
	ListOpenOrders(ctx context.Context, symbol string) ([]Order, error)
	ListOrders(ctx context.Context, symbol string, status OrderStatus) ([]Order, error)
	GetFeeRates(ctx context.Context, symbol string, market MarketType) (FeeRates, error)
	PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	GetOrder(ctx context.Context, symbol, orderID string) (Order, error)

	Ping(ctx context.Context) error
	ServerTime(ctx context.Context) (time.Time, error)
}
```

## API reference

### Enums

```go
type ExchangeName string
const (
	ExchangeBinance ExchangeName = "Binance"
	ExchangeBybit   ExchangeName = "Bybit"
	ExchangeDydx    ExchangeName = "Dydx"
)

type CandleInterval string
const (
	CandleIntervalTick   CandleInterval = "tick"
	CandleIntervalSecond CandleInterval = "1s"
	CandleIntervalMinute CandleInterval = "1m"
	CandleIntervalHour   CandleInterval = "1h"
	CandleIntervalDay    CandleInterval = "1d"
)

type MarketType string
const (
	MarketSpot        MarketType = "spot"
	MarketDerivatives MarketType = "derivatives"
)

type OrderStatus string
const (
	OrderStatusNew             OrderStatus = "NEW"
	OrderStatusPartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	OrderStatusFilled          OrderStatus = "FILLED"
	OrderStatusCanceled        OrderStatus = "CANCELED"
	OrderStatusRejected        OrderStatus = "REJECTED"
)

type OrderSide string
const (
	OrderSideBuy  OrderSide = "BUY"
	OrderSideSell OrderSide = "SELL"
)

type OrderType string
const (
	OrderTypeLimit  OrderType = "LIMIT"
	OrderTypeMarket OrderType = "MARKET"
)

type TimeInForce string
const (
	TimeInForceGTC TimeInForce = "GTC"
	TimeInForceIOC TimeInForce = "IOC"
	TimeInForceFOK TimeInForce = "FOK"
)
```

### Structs

```go
type Capabilities struct {
	Spot        bool
	Derivatives bool
	Streaming   bool
}

type Config struct {
	Exchange   ExchangeName // required
	APIKey     string
	APISecret  string
	Passphrase string        // dYdX only
	BaseURL    string        // optional override
	Timeout    time.Duration // default 10s

	// dYdX-only fields
	EthereumAddress          string
	StarkPublicKey           string
	StarkPrivateKey          string
	StarkPublicKeyYCoordinate string
}

type PlaceOrderRequest struct {
	Symbol        string      // required
	Market        MarketType  // optional: spot/derivatives
	Leverage      string      // optional
	Side          OrderSide    // BUY/SELL
	Type          OrderType    // LIMIT/MARKET
	Quantity      string      // required
	Price         string      // required for LIMIT
	TimeInForce   TimeInForce // GTC/IOC/FOK (LIMIT only)
	ClientOrderID string      // optional
	ReduceOnly    bool        // derivatives only
}

type Order struct {
	ID        string
	Symbol    string
	Market    MarketType
	Side      OrderSide
	Type      OrderType
	Status    OrderStatus
	Quantity  string
	Filled    string
	Price     string
	AvgPrice  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Balance struct {
	Asset  string
	Free   string
	Locked string
	Total  string
}

type Candle struct {
	Symbol    string
	Interval  CandleInterval
	OpenTime  time.Time
	CloseTime time.Time
	Open      string
	High      string
	Low       string
	Close     string
	Volume    string
	Trades    string
}
```

### Methods

- `Name() ExchangeName`  
  Returns the exchange identifier.
- `Capabilities() Capabilities`  
  Describes spot/derivatives/streaming support.
- `SubscribeCandles(ctx, symbol, interval)`  
  Returns two channels. Candles are delivered via polling. The error channel is buffered and may report intermittent issues.
- `GetCandles(ctx, symbol, interval, start, end)`  
  Loads historical candles in the given time range (inclusive bounds are exchange-dependent).
- `GetBalances(ctx)`  
  Returns unified balances for the account.
- `ListOpenOrders(ctx, symbol)`  
  Returns open orders. Some exchanges require a non-empty symbol.
- `ListOrders(ctx, symbol, status)`  
  Returns order history, optionally filtered by unified status. Pass `status == ""` to disable filtering.
- `PlaceOrder(ctx, req)`  
  Places a new order and returns the unified `Order`.
- `CancelOrder(ctx, symbol, orderID)`  
  Cancels by exchange order ID or client order ID (Binance).
- `GetOrder(ctx, symbol, orderID)`  
  Returns a single order by ID.
- `Ping(ctx)`  
  Health check.
- `ServerTime(ctx)`  
  Returns exchange server time.

## Examples

### Balances

```go
balances, err := ex.GetBalances(ctx)
if err != nil {
	panic(err)
}
for _, b := range balances {
	fmt.Printf("%s free=%s locked=%s total=%s\n", b.Asset, b.Free, b.Locked, b.Total)
}
```

### Place a limit order

```go
order, err := ex.PlaceOrder(ctx, broker.PlaceOrderRequest{
	Symbol:      "BTCUSDT",
	Side:        broker.OrderSideBuy,
	Type:        broker.OrderTypeLimit,
	Quantity:    "0.001",
	Price:       "25000",
	TimeInForce: broker.TimeInForceGTC,
})
if err != nil {
	panic(err)
}
fmt.Println("order id:", order.ID)
```

### Order history

```go
orders, err := ex.ListOrders(ctx, "BTCUSDT", broker.OrderStatusFilled)
if err != nil {
	panic(err)
}
fmt.Println("filled orders:", len(orders))
```

### Cancel and get order

```go
if err := ex.CancelOrder(ctx, "BTCUSDT", "123456"); err != nil {
	panic(err)
}

order, err := ex.GetOrder(ctx, "BTCUSDT", "123456")
if err != nil {
	panic(err)
}
fmt.Println("status:", order.Status)
```

### Candles (polling subscription)

```go
candles, errs := ex.SubscribeCandles(ctx, "BTCUSDT", broker.CandleIntervalMinute)
for {
	select {
	case c := <-candles:
		fmt.Printf("kline %s %s o=%s c=%s\n", c.Symbol, c.Interval, c.Open, c.Close)
	case err := <-errs:
		fmt.Println("candle error:", err)
	}
}
```

### Candles (historical)

```go
end := time.Now()
start := end.Add(-6 * time.Hour)
items, err := ex.GetCandles(ctx, "BTCUSDT", broker.CandleIntervalMinute, start, end)
if err != nil {
	panic(err)
}
fmt.Println("candles:", len(items))
```

## Errors

Common errors are exposed for uniform handling:
- `ErrNotSupported`
- `ErrOrderNotFound`
- `ErrAuth`
- `ErrRateLimited`
- `ErrInsufficientBalance`
- `ErrInvalidConfig`

The adapters map SDK errors to these where possible.

## Candle intervals

Available enum values:
- `CandleIntervalTick` (trade ticks from recent trades)
- `CandleIntervalSecond` (1s candles from aggregated trades)
- `CandleIntervalMinute`
- `CandleIntervalHour`
- `CandleIntervalDay`

## Notes and limitations

- Symbol format must match the target exchange (e.g. `BTCUSDT` for Binance/Bybit, `BTC-USD` for dYdX).
- `SubscribeCandles` uses REST polling under the hood (no websocket stream).
- Tick and 1s candles are derived from recent trades and may be incomplete for large historical ranges.
- `ListOrders`, `GetOrder`, and `CancelOrder` return `ErrInvalidConfig` if `symbol` is empty.
- `ListOrders`, `GetOrder`, and `CancelOrder` require a non-empty `symbol` across all adapters.
- dYdX balances are returned as a single USDC-like asset derived from account equity/collateral.

## Tests

Run unit tests:

```bash
go test ./...
```

Live smoke tests (requires env vars, see `contract_test.go`):

```bash
BROKER_LIVE_TESTS=1 go test ./...
```
