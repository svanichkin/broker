package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNotSupported        = errors.New("broker: not supported")
	ErrOrderNotFound       = errors.New("broker: order not found")
	ErrAuth                = errors.New("broker: auth failed")
	ErrRateLimited         = errors.New("broker: rate limited")
	ErrInsufficientBalance = errors.New("broker: insufficient balance")
	ErrInvalidConfig       = errors.New("broker: invalid config")
)

func wrapError(err error, kind error) error {
	if err == nil || kind == nil {
		return err
	}
	if errors.Is(err, kind) {
		return err
	}
	return fmt.Errorf("%w: %v", kind, err)
}

func mapCommonError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many") || strings.Contains(msg, "429"):
		return wrapError(err, ErrRateLimited)
	case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "api key") ||
		strings.Contains(msg, "signature") || strings.Contains(msg, "auth"):
		return wrapError(err, ErrAuth)
	case strings.Contains(msg, "insufficient") || strings.Contains(msg, "not enough"):
		return wrapError(err, ErrInsufficientBalance)
	case strings.Contains(msg, "order not found") || strings.Contains(msg, "unknown order") ||
		strings.Contains(msg, "not found"):
		return wrapError(err, ErrOrderNotFound)
	default:
		return err
	}
}
