// Package resource carries provider-reported usage and budget checks across runners.
package resource

import (
	"context"
	"errors"
	"math"
)

var ErrBudgetExceeded = errors.New("resource budget exhausted")

type Usage struct {
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

func (u Usage) Valid() bool {
	return u.Tokens >= 0 && u.CostUSD >= 0 && !math.IsNaN(u.CostUSD) && !math.IsInf(u.CostUSD, 0)
}

type hooks struct {
	check  func(context.Context) error
	report func(context.Context, Usage) error
}
type contextKey struct{}

func WithBudget(ctx context.Context, check func(context.Context) error, report func(context.Context, Usage) error) context.Context {
	return context.WithValue(ctx, contextKey{}, hooks{check, report})
}

func Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h, ok := ctx.Value(contextKey{}).(hooks); ok && h.check != nil {
		return h.check(ctx)
	}
	return nil
}

func Report(ctx context.Context, usage Usage) error {
	if !usage.Valid() {
		return errors.New("invalid provider usage")
	}
	if h, ok := ctx.Value(contextKey{}).(hooks); ok && h.report != nil {
		return h.report(ctx, usage)
	}
	return nil
}

// Tokens includes cache reads and writes for Claude, whose input_tokens excludes
// them. Codex and Responses already include cached tokens in input_tokens.
type ProviderUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

func (u ProviderUsage) Total() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

type limiterKey struct{}

// WithConcurrency shares one pool between model requests and external CLI runs.
func WithConcurrency(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, limiterKey{}, make(chan struct{}, max(1, limit)))
}

func Acquire(ctx context.Context) (func(), error) {
	if err := Check(ctx); err != nil {
		return nil, err
	}
	slots, ok := ctx.Value(limiterKey{}).(chan struct{})
	if !ok {
		return func() {}, nil
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-slots }
	if err := Check(ctx); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
