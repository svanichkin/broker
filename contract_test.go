package broker

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestInterfaceCompliance(t *testing.T) {
	var _ Exchange = (*binanceClient)(nil)
	var _ Exchange = (*bybitClient)(nil)
	var _ Exchange = (*dydxClient)(nil)
}

func TestSmoke(t *testing.T) {
	if os.Getenv("BROKER_LIVE_TESTS") == "" {
		t.Skip("set BROKER_LIVE_TESTS=1 to run live smoke tests")
	}
	cases := []struct {
		name   ExchangeName
		prefix string
	}{
		{ExchangeBinance, "BINANCE"},
		{ExchangeBybit, "BYBIT"},
		{ExchangeDydx, "DYDX"},
	}
	for _, c := range cases {
		cfg, ok := configFromEnv(c.name, c.prefix)
		if !ok {
			t.Skipf("missing %s_* env vars", c.prefix)
		}
		ex, err := NewExchange(context.Background(), cfg)
		if err != nil {
			t.Fatalf("%s new: %v", c.name, err)
		}
		if err := ex.Ping(context.Background()); err != nil {
			t.Fatalf("%s ping: %v", c.name, err)
		}
		if _, err := ex.ServerTime(context.Background()); err != nil {
			t.Fatalf("%s server time: %v", c.name, err)
		}
	}
}

func configFromEnv(name ExchangeName, prefix string) (Config, bool) {
	key := os.Getenv(prefix + "_API_KEY")
	secret := os.Getenv(prefix + "_API_SECRET")
	passphrase := os.Getenv(prefix + "_PASSPHRASE")
	baseURL := os.Getenv(prefix + "_BASE_URL")
	timeout := 10 * time.Second
	if key == "" && secret == "" && name != ExchangeDydx {
		return Config{}, false
	}
	cfg := Config{
		Exchange:   name,
		APIKey:     key,
		APISecret:  secret,
		Passphrase: passphrase,
		BaseURL:    baseURL,
		Timeout:    timeout,
	}
	if name == ExchangeDydx {
		cfg.EthereumAddress = os.Getenv(prefix + "_ETH_ADDRESS")
		cfg.StarkPublicKey = os.Getenv(prefix + "_STARK_PUBLIC_KEY")
		cfg.StarkPrivateKey = os.Getenv(prefix + "_STARK_PRIVATE_KEY")
		cfg.StarkPublicKeyYCoordinate = os.Getenv(prefix + "_STARK_PUBLIC_KEY_Y")
		if cfg.EthereumAddress == "" || cfg.StarkPublicKey == "" || cfg.StarkPrivateKey == "" || cfg.StarkPublicKeyYCoordinate == "" {
			return Config{}, false
		}
	}
	return cfg, true
}
