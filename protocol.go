package broker

import (
	"context"
	"time"
)

type ExchangeName string

const (
	ExchangeBinance ExchangeName = "Binance"
	ExchangeBybit   ExchangeName = "Bybit"
	ExchangeDydx    ExchangeName = "Dydx"
)

type Capabilities struct {
	Spot        bool
	Derivatives bool
	Streaming   bool
}

type CandleInterval string

const (
	CandleIntervalTick   CandleInterval = "tick"
	CandleIntervalSecond CandleInterval = "1s"
	CandleIntervalMinute CandleInterval = "1m"
	CandleIntervalHour   CandleInterval = "1h"
	CandleIntervalDay    CandleInterval = "1d"
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

type Order struct {
	ID        string
	Symbol    string
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

type PlaceOrderRequest struct {
	Symbol        string
	Side          OrderSide
	Type          OrderType
	Quantity      string
	Price         string
	TimeInForce   TimeInForce
	ClientOrderID string
	ReduceOnly    bool
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

type Config struct {
	Exchange   ExchangeName
	APIKey     string
	APISecret  string
	Passphrase string
	BaseURL    string
	Timeout    time.Duration
	// dYdX-specific credentials
	EthereumAddress          string
	StarkPublicKey           string
	StarkPrivateKey          string
	StarkPublicKeyYCoordinate string
}

type Exchange interface {
	Name() ExchangeName
	Capabilities() Capabilities

	SubscribeCandles(ctx context.Context, symbol string, interval CandleInterval) (<-chan Candle, <-chan error)
	GetCandles(ctx context.Context, symbol string, interval CandleInterval, start, end time.Time) ([]Candle, error)

	GetBalances(ctx context.Context) ([]Balance, error)
	ListOpenOrders(ctx context.Context, symbol string) ([]Order, error)
	ListOrders(ctx context.Context, symbol string, status OrderStatus) ([]Order, error)
	PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	GetOrder(ctx context.Context, symbol, orderID string) (Order, error)

	Ping(ctx context.Context) error
	ServerTime(ctx context.Context) (time.Time, error)
}
