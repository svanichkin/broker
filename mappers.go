package broker

import (
	"context"
	"sort"
	"strconv"
	"time"
)

func intervalDuration(interval CandleInterval) (time.Duration, error) {
	switch interval {
	case CandleIntervalTick:
		return 0, ErrNotSupported
	case CandleIntervalSecond:
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

type timeRange struct {
	Start time.Time
	End   time.Time
}

func splitCandleRange(start, end time.Time, interval CandleInterval, max int) ([]timeRange, error) {
	if max <= 0 {
		return []timeRange{{Start: start, End: end}}, nil
	}
	dur, err := intervalDuration(interval)
	if err != nil || dur == 0 {
		return []timeRange{{Start: start, End: end}}, err
	}
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return []timeRange{{Start: start, End: end}}, nil
	}
	ranges := make([]timeRange, 0, 1)
	maxSpan := dur * time.Duration(max-1)
	for curStart := start; !curStart.After(end); {
		curEnd := curStart.Add(maxSpan)
		if curEnd.After(end) {
			curEnd = end
		}
		ranges = append(ranges, timeRange{Start: curStart, End: curEnd})
		if curEnd.Equal(end) {
			break
		}
		curStart = curEnd.Add(dur)
	}
	return ranges, nil
}

func closedCandleBoundary(end time.Time) time.Time {
	now := time.Now().UTC()
	if end.IsZero() || end.After(now) {
		return now
	}
	return end
}

func filterClosedCandles(candles []Candle, end time.Time) []Candle {
	if len(candles) == 0 {
		return candles
	}
	boundary := closedCandleBoundary(end)
	out := make([]Candle, 0, len(candles))
	for _, candle := range candles {
		if candle.CloseTime.After(boundary) {
			continue
		}
		out = append(out, candle)
	}
	return out
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

type tradeTick struct {
	Time  time.Time
	Price float64
	Size  float64
}

func tickCandles(symbol string, trades []tradeTick) []Candle {
	if len(trades) == 0 {
		return nil
	}
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].Time.Before(trades[j].Time)
	})
	out := make([]Candle, 0, len(trades))
	for _, t := range trades {
		out = append(out, Candle{
			Symbol:    symbol,
			Interval:  CandleIntervalTick,
			OpenTime:  t.Time,
			CloseTime: t.Time,
			Open:      formatFloat(t.Price),
			High:      formatFloat(t.Price),
			Low:       formatFloat(t.Price),
			Close:     formatFloat(t.Price),
			Volume:    formatFloat(t.Size),
			Trades:    "1",
		})
	}
	return out
}

func aggregateSecondCandles(symbol string, trades []tradeTick) []Candle {
	if len(trades) == 0 {
		return nil
	}
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].Time.Before(trades[j].Time)
	})

	out := make([]Candle, 0)
	curSec := trades[0].Time.Unix()
	open := trades[0].Price
	high := trades[0].Price
	low := trades[0].Price
	closePrice := trades[0].Price
	volume := trades[0].Size
	tradeCount := 1

	flush := func(sec int64) {
		start := time.Unix(sec, 0)
		out = append(out, Candle{
			Symbol:    symbol,
			Interval:  CandleIntervalSecond,
			OpenTime:  start,
			CloseTime: start.Add(time.Second),
			Open:      formatFloat(open),
			High:      formatFloat(high),
			Low:       formatFloat(low),
			Close:     formatFloat(closePrice),
			Volume:    formatFloat(volume),
			Trades:    strconv.Itoa(tradeCount),
		})
	}

	for i := 1; i < len(trades); i++ {
		sec := trades[i].Time.Unix()
		if sec != curSec {
			flush(curSec)
			curSec = sec
			open = trades[i].Price
			high = trades[i].Price
			low = trades[i].Price
			closePrice = trades[i].Price
			volume = trades[i].Size
			tradeCount = 1
			continue
		}
		if trades[i].Price > high {
			high = trades[i].Price
		}
		if trades[i].Price < low {
			low = trades[i].Price
		}
		closePrice = trades[i].Price
		volume += trades[i].Size
		tradeCount++
	}
	flush(curSec)
	return out
}
