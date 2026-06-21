package jmap

import "fmt"

// HTTPError is returned when the JMAP server responds with a non-200 status.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("jmap http %d: %s", e.StatusCode, truncate(e.Body, 300))
}

// Retryable reports whether the HTTP error is transient (429 / 5xx).
func (e *HTTPError) Retryable() bool {
	return e.StatusCode == 429 || e.StatusCode >= 500
}

// MethodError is a JMAP method-level error (an "error" response invocation).
type MethodError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	// RawArgs holds the full error JSON for debugging when Description is empty.
	RawArgs string `json:"-"`
}

func (e *MethodError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("jmap method error %q: %s", e.Type, e.Description)
	}
	if e.RawArgs != "" {
		return fmt.Sprintf("jmap method error %q (raw: %s)", e.Type, e.RawArgs)
	}
	return fmt.Sprintf("jmap method error %q", e.Type)
}

// Retryable reports whether retrying could help. Method-level errors (bad
// arguments, cannotCalculateChanges, …) are deterministic, so they are not.
func (e *MethodError) Retryable() bool { return false }

// IsCannotCalculateChanges reports whether err is the JMAP
// "cannotCalculateChanges" method error (state token too old) — the caller
// should fall back to a full query.
func IsCannotCalculateChanges(err error) bool {
	me, ok := err.(*MethodError)
	return ok && me.Type == "cannotCalculateChanges"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
