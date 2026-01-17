package broker

import (
	"context"
	"strconv"
	"time"
)

func intervalDuration(interval CandleInterval) (time.Duration, error) {
	switch interval {
	case CandleIntervalTick:
		return time.Second, nil
	case CandleIntervalMinute:
		return time.Minute, nil
	case CandleIntervalHour:
		return time.Hour, nil
	case CandleIntervalDay:
		return 24 * time.Hour, nil
	default:
		return 0, ErrNotSupported
	}
}

func subscribeByPolling(ctx context.Context, interval time.Duration, fetch func(context.Context) (Candle, error)) (<-chan Candle, <-chan error) {
	out := make(chan Candle)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			candle, err := fetch(ctx)
			if err != nil {
				select {
				case errs <- err:
				default:
				}
			} else {
				select {
				case out <- candle:
				case <-ctx.Done():
					return
				}
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

func parseFloat(val string) (float64, error) {
	return strconv.ParseFloat(val, 64)
}

func formatFloat(val float64) string {
	return strconv.FormatFloat(val, 'f', -1, 64)
}

func channelWithError(err error) (<-chan Candle, <-chan error) {
	out := make(chan Candle)
	errs := make(chan error, 1)
	if err != nil {
		errs <- err
	}
	close(out)
	close(errs)
	return out, errs
}
