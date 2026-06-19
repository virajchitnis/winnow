// Package unsubscribe parses List-Unsubscribe headers and executes the safe,
// standardized unsubscribe methods (RFC 8058 One-Click and mailto:). Bare HTTPS
// links are never auto-fetched — they are surfaced for the user to open
// manually.
package unsubscribe

import (
	"strings"

	"winnow/internal/store"
)

// Parse interprets the List-Unsubscribe and List-Unsubscribe-Post headers and
// returns the preferred method and its target.
//
//   - method == store.UnsubMethodOneClick: target is the HTTPS URL to POST to
//     (RFC 8058) — safe to auto-execute.
//   - method == store.UnsubMethodMailto: target is the mailto target (address
//     plus any ?subject/&body query) — safe to auto-execute via JMAP.
//   - method == store.UnsubMethodHTTP: target is a bare HTTPS URL with no
//     One-Click support — shown for manual action, never auto-fetched.
//   - method == "": no usable unsubscribe mechanism.
func Parse(listUnsubscribe, listUnsubscribePost string) (method, target string) {
	var httpsURL, mailtoTarget string
	for _, entry := range bracketed(listUnsubscribe) {
		switch {
		case strings.HasPrefix(strings.ToLower(entry), "https://"):
			if httpsURL == "" {
				httpsURL = entry
			}
		case strings.HasPrefix(strings.ToLower(entry), "mailto:"):
			if mailtoTarget == "" {
				mailtoTarget = entry[len("mailto:"):]
			}
		}
	}

	oneClick := strings.Contains(
		strings.ToLower(strings.ReplaceAll(listUnsubscribePost, " ", "")),
		"list-unsubscribe=one-click")

	switch {
	case httpsURL != "" && oneClick:
		return store.UnsubMethodOneClick, httpsURL
	case mailtoTarget != "":
		return store.UnsubMethodMailto, mailtoTarget
	case httpsURL != "":
		return store.UnsubMethodHTTP, httpsURL
	default:
		return "", ""
	}
}

// bracketed extracts the values inside <...> from a header, tolerating
// comma/whitespace separators and entries without brackets.
func bracketed(header string) []string {
	var out []string
	s := header
	for {
		open := strings.IndexByte(s, '<')
		if open < 0 {
			break
		}
		close := strings.IndexByte(s[open+1:], '>')
		if close < 0 {
			break
		}
		val := strings.TrimSpace(s[open+1 : open+1+close])
		if val != "" {
			out = append(out, val)
		}
		s = s[open+1+close+1:]
	}
	if len(out) == 0 {
		// No brackets: treat the whole header as a single bare value.
		if v := strings.TrimSpace(header); v != "" {
			out = append(out, v)
		}
	}
	return out
}
