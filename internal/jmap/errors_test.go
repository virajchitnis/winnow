package jmap

import "testing"

func TestHTTPError(t *testing.T) {
	e := &HTTPError{StatusCode: 503, Body: "down"}
	if e.Error() == "" || !e.Retryable() {
		t.Errorf("503 should be retryable: %q %v", e.Error(), e.Retryable())
	}
	if (&HTTPError{StatusCode: 400}).Retryable() {
		t.Error("400 should not be retryable")
	}
}

func TestMethodError(t *testing.T) {
	e := &MethodError{Type: "invalidArguments", Description: "bad"}
	if e.Error() == "" || e.Retryable() {
		t.Errorf("method error: %q retryable=%v", e.Error(), e.Retryable())
	}
	if (&MethodError{Type: "x"}).Error() == "" {
		t.Error("error string without description should still render")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short = %q", got)
	}
	if got := truncate("hello world", 5); got != "hello…" {
		t.Errorf("long = %q", got)
	}
}
