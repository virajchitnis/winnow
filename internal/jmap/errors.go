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

// MethodError is a JMAP method-level error (an "error" response invocation).
type MethodError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func (e *MethodError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("jmap method error %q: %s", e.Type, e.Description)
	}
	return fmt.Sprintf("jmap method error %q", e.Type)
}

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
