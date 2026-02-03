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
	OrderStatusUnknown         OrderStatus = "UNKNOWN"
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
	ID             string
	Symbol         string
	Market         MarketType
	Side           OrderSide
	Type           OrderType
	Status         OrderStatus
	Quantity       string
	Filled         string
	Price          string
	AvgPrice       string
	CumExecFee     string
	ReduceOnly     bool
	CloseOnTrigger bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type FeeRates struct {
	Maker string
	Taker string
}

type Balance struct {
	Asset  string
	Free   string
	Locked string
	Total  string
}

type PlaceOrderRequest struct {
	Symbol        string
	Market        MarketType
	Leverage      string
	Side          OrderSide
	Type          OrderType
	Quantity      string
	Price         string
	TimeInForce   TimeInForce
	ClientOrderID string
	ReduceOnly    bool
	PositionIdx   string
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
	EthereumAddress           string
	StarkPublicKey            string
	StarkPrivateKey           string
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
	GetFeeRates(ctx context.Context, symbol string, market MarketType) (FeeRates, error)
	PlaceOrder(ctx context.Context, req PlaceOrderRequest) (Order, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	GetOrder(ctx context.Context, symbol, orderID string) (Order, error)

	Ping(ctx context.Context) error
	ServerTime(ctx context.Context) (time.Time, error)
}
