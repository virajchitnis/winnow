package unsubscribe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"winnow/internal/jmap"
	"winnow/internal/store"
)

// ErrManual is returned for bare-HTTPS unsubscribe links, which are NEVER
// auto-fetched (fetching arbitrary email URLs is a tracking/phishing vector).
// The dashboard shows the link for the user to open manually.
var ErrManual = errors.New("unsubscribe: bare HTTPS link requires manual action")

// Doer is the HTTP client used for One-Click POSTs (injectable for tests).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Mailer sends the mailto: unsubscribe email via JMAP.
type Mailer interface {
	PrimaryIdentity(ctx context.Context) (jmap.Identity, error)
	SendEmail(ctx context.Context, msg jmap.OutgoingMessage) error
}

// Executor performs approved unsubscribes via the safe methods only.
type Executor struct {
	http Doer
	mail Mailer
}

// NewExecutor returns an Executor. A nil Doer uses a default client that does
// not follow cross-host redirects.
func NewExecutor(mail Mailer, opts ...Option) *Executor {
	e := &Executor{mail: mail, http: safeHTTPClient()}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Option configures an Executor.
type Option func(*Executor)

// WithDoer overrides the HTTP client (used in tests).
func WithDoer(d Doer) Option { return func(e *Executor) { e.http = d } }

// Execute performs the unsubscribe for the given method/target. Bare HTTPS
// links return ErrManual and are never fetched.
func (e *Executor) Execute(ctx context.Context, method, target string) error {
	switch method {
	case store.UnsubMethodOneClick:
		return e.oneClick(ctx, target)
	case store.UnsubMethodMailto:
		return e.mailto(ctx, target)
	case store.UnsubMethodHTTP:
		return ErrManual
	default:
		return fmt.Errorf("unsubscribe: unknown method %q", method)
	}
}

// oneClick performs an RFC 8058 One-Click unsubscribe: POST the fixed body to
// the HTTPS URL. Only https is allowed; redirects to other hosts are refused.
func (e *Executor) oneClick(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("unsubscribe: bad URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return fmt.Errorf("unsubscribe: refusing non-https one-click target %q", rawURL)
	}
	body := strings.NewReader("List-Unsubscribe=One-Click")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := e.http.Do(req)
	if err != nil {
		return fmt.Errorf("unsubscribe one-click: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unsubscribe one-click: status %d", resp.StatusCode)
	}
	return nil
}

// mailto sends the unsubscribe email. The target is the mailto value (address
// plus optional ?subject/&body query).
func (e *Executor) mailto(ctx context.Context, target string) error {
	addr, subject, bodyText := parseMailto(target)
	if addr == "" {
		return fmt.Errorf("unsubscribe: empty mailto target")
	}
	ident, err := e.mail.PrimaryIdentity(ctx)
	if err != nil {
		return err
	}
	if subject == "" {
		subject = "unsubscribe"
	}
	return e.mail.SendEmail(ctx, jmap.OutgoingMessage{
		FromIdentity: ident,
		To:           []string{addr},
		Subject:      subject,
		Text:         bodyText,
	})
}

// parseMailto splits a mailto target into address, subject, and body.
func parseMailto(target string) (addr, subject, body string) {
	q := ""
	if i := strings.IndexByte(target, '?'); i >= 0 {
		addr, q = target[:i], target[i+1:]
	} else {
		addr = target
	}
	addr = strings.TrimSpace(addr)
	if q == "" {
		return addr, "", ""
	}
	values, err := url.ParseQuery(q)
	if err != nil {
		return addr, "", ""
	}
	return addr, values.Get("subject"), values.Get("body")
}

func safeHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			// Only allow redirects that stay on the original host.
			if req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("unsubscribe: refusing cross-host redirect to %q", req.URL.Host)
			}
			if len(via) >= 5 {
				return fmt.Errorf("unsubscribe: too many redirects")
			}
			return nil
		},
	}
}
