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
	PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	GetOrder(ctx context.Context, symbol, orderID string) (Order, error)

	Ping(ctx context.Context) error
	ServerTime(ctx context.Context) (time.Time, error)
}
```

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

Supported intervals:
- `CandleIntervalTick`
- `CandleIntervalMinute`
- `CandleIntervalHour`
- `CandleIntervalDay`

## Notes and limitations

- `SubscribeCandles` uses REST polling under the hood (no websocket stream).
- Bybit open orders require a `symbol` (`ListOpenOrders` returns `ErrNotSupported` if empty).
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
