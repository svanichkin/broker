package broker

import (
	"context"
	"strings"
	"time"
)

type Facade struct {
	Exchange
}

func New(ctx context.Context, cfg Config) (*Facade, error) {
	ex, err := NewExchange(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Facade{Exchange: ex}, nil
}

func NewExchange(ctx context.Context, cfg Config) (Exchange, error) {
	_ = ctx
	if cfg.Exchange == "" {
		return nil, ErrInvalidConfig
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	switch normalizeExchangeName(cfg.Exchange) {
	case ExchangeBinance:
		return newBinance(cfg)
	case ExchangeBybit:
		return newBybit(cfg)
	case ExchangeDydx:
		return newDydx(cfg)
	default:
		return nil, ErrInvalidConfig
	}
}

func normalizeExchangeName(name ExchangeName) ExchangeName {
	switch strings.ToLower(string(name)) {
	case "binance":
		return ExchangeBinance
	case "bybit":
		return ExchangeBybit
	case "dydx", "dy/dx", "dydxv3":
		return ExchangeDydx
	default:
		return name
	}
}
