package classify

import (
	"errors"
	"fmt"
)

// ErrRefused is returned when the model declines the request (stop_reason
// "refusal"). The caller treats it like any classification failure and leaves
// the affected mail in the inbox.
var ErrRefused = errors.New("anthropic: request refused")

// APIError is a non-200 response from the Anthropic API.
type APIError struct {
	StatusCode int
	Body       string
	RetryAfter string // value of the Retry-After header, if any
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("anthropic http %d: %s", e.StatusCode, b)
}

// Retryable reports whether the error is worth retrying (429 / 5xx).
func (e *APIError) Retryable() bool {
	return e.StatusCode == 429 || e.StatusCode >= 500
}
