package resource

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestPoolIsSharedAndChecksBudgetAfterWaiting(t *testing.T) {
	pool := WithConcurrency(context.Background(), 1)
	release, err := Acquire(pool)
	if err != nil {
		t.Fatal(err)
	}
	var exhausted atomic.Bool
	waiting := WithBudget(pool, func(context.Context) error {
		if exhausted.Load() {
			return ErrBudgetExceeded
		}
		return nil
	}, nil)
	done := make(chan error, 1)
	go func() {
		release, err := Acquire(waiting)
		if err == nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("second request passed occupied slot: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	exhausted.Store(true)
	release()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("waiting request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request stuck")
	}
	// Cancellation does not consume a slot.
	release, err = Acquire(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(pool)
	cancel()
	if _, err := Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestUsageReaderPreservesBytesAndReportsOnce(t *testing.T) {
	for _, line := range []string{
		`{"usage":{"total_tokens":12,"cost":0.5}}`,
		`{"response":{"usage":{"input_tokens":10,"output_tokens":2,"cost":0.5}}}`,
	} {
		t.Run(line, func(t *testing.T) {
			body := "data: {\"delta\":\"text\"}\n\ndata: " + line + "\n\ndata: [DONE]\n\n"
			reports := 0
			reader := &UsageReader{Body: io.NopCloser(strings.NewReader(body)), Report: func(u Usage) error {
				reports++
				if u.Tokens != 12 || u.CostUSD != 0.5 {
					t.Fatalf("usage: %+v", u)
				}
				return nil
			}}
			data, err := io.ReadAll(reader)
			if err != nil || string(data) != body || reports != 1 {
				t.Fatalf("read %q, %v, %d reports", data, err, reports)
			}
		})
	}
}

func TestUsageReaderReturnsBudgetError(t *testing.T) {
	reader := &UsageReader{Body: io.NopCloser(strings.NewReader("data: {\"usage\":{\"total_tokens\":10}}\n\n")), Report: func(Usage) error { return ErrBudgetExceeded }}
	if _, err := io.ReadAll(reader); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("error: %v", err)
	}
}
