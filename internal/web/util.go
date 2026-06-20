package web

import (
	"context"
	"strings"
)

// contextDetached returns a background context for work that must outlive the
// HTTP request (run-now / sweep started from a button).
func contextDetached() context.Context { return context.Background() }

// domainOf returns the lowercased domain part of an email address, or "".
func domainOf(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[at+1:])
}
