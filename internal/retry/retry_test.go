package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

type permanentErr struct{}

func (permanentErr) Error() string   { return "permanent" }
func (permanentErr) Retryable() bool { return false }

func fastPolicy() Policy {
	return Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
}

func TestDoSucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastPolicy(), func() error {
		calls++
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDoRetriesThenSucceeds(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastPolicy(), func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d (want 3)", err, calls)
	}
}

func TestDoStopsOnNonRetryable(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastPolicy(), func() error {
		calls++
		return permanentErr{}
	})
	if err == nil || calls != 1 {
		t.Fatalf("non-retryable should not retry: err=%v calls=%d", err, calls)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastPolicy(), func() error {
		calls++
		return errors.New("always")
	})
	if err == nil || calls != 4 {
		t.Fatalf("should exhaust 4 attempts: err=%v calls=%d", err, calls)
	}
}

func TestDoRespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Do(ctx, Policy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: time.Second}, func() error {
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
