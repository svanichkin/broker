package broker

import (
	"testing"
	"time"
)

func TestNormalizeCandleRequestEnd(t *testing.T) {
	t.Run("boundary shifts by 1ms", func(t *testing.T) {
		end := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
		got := normalizeCandleRequestEnd(end, CandleIntervalMinute)
		want := end.Add(-time.Millisecond)
		if !got.Equal(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("non-boundary unchanged", func(t *testing.T) {
		end := time.Date(2026, 3, 3, 12, 0, 1, 0, time.UTC)
		got := normalizeCandleRequestEnd(end, CandleIntervalMinute)
		if !got.Equal(end) {
			t.Fatalf("got %v, want %v", got, end)
		}
	})

	t.Run("zero unchanged", func(t *testing.T) {
		got := normalizeCandleRequestEnd(time.Time{}, CandleIntervalMinute)
		if !got.IsZero() {
			t.Fatalf("got %v, want zero time", got)
		}
	})
}

func TestFilterCandlesByOpenTime_IncludesRightBoundary(t *testing.T) {
	start := time.Date(2021, 10, 31, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 11, 1, 0, 0, 0, 0, time.UTC)
	c := Candle{
		Symbol:    "ENJUSDT",
		Interval:  CandleIntervalDay,
		OpenTime:  end,
		CloseTime: end.Add(24 * time.Hour),
	}

	got := filterCandlesByOpenTime([]Candle{c}, start, end)
	if len(got) != 1 {
		t.Fatalf("expected 1 candle, got %d", len(got))
	}
}
