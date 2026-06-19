// Package retry provides exponential-backoff retries for transient JMAP and
// Anthropic failures.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

// retryabler lets an error opt out of retries (e.g. a 4xx). Errors without this
// method are retried by default, so transient network failures are covered.
type retryabler interface {
	Retryable() bool
}

// Policy configures retry behavior.
type Policy struct {
	MaxAttempts int           // total attempts including the first (default 4)
	BaseDelay   time.Duration // initial backoff (default 500ms)
	MaxDelay    time.Duration // cap on backoff (default 30s)
}

// DefaultPolicy is a sensible default for background work.
var DefaultPolicy = Policy{MaxAttempts: 4, BaseDelay: 500 * time.Millisecond, MaxDelay: 30 * time.Second}

// Do runs fn, retrying on retryable errors with exponential backoff + jitter.
// It returns the last error, or ctx.Err() if the context is cancelled.
func Do(ctx context.Context, p Policy, fn func() error) error {
	if p.MaxAttempts <= 0 {
		p = DefaultPolicy
	}
	var err error
	delay := p.BaseDelay
	if delay <= 0 {
		delay = DefaultPolicy.BaseDelay
	}
	maxDelay := p.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultPolicy.MaxDelay
	}

	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !retryable(err) || attempt == p.MaxAttempts {
			return err
		}
		// Sleep with jitter, honoring context cancellation.
		sleep := delay + time.Duration(rand.Int63n(int64(delay)+1))
		if sleep > maxDelay {
			sleep = maxDelay
		}
		t := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	return err
}

func retryable(err error) bool {
	var r retryabler
	if errors.As(err, &r) {
		return r.Retryable()
	}
	return true // default: retry (covers transient network errors)
}
