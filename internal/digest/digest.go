// Package digest composes and sends the once-a-day summary email. Its arrival
// also serves as a heartbeat: if it stops coming, something is wrong.
package digest

import (
	"context"
	"time"

	"winnow/internal/jmap"
	"winnow/internal/store"
)

// Store is the persistence surface the digest reads.
type Store interface {
	DecisionsSince(cutoff string) ([]store.Decision, error)
	ActiveErrors(limit int) ([]store.AppError, error)
}

// Mailer sends the digest via JMAP. The recipient is derived from the account
// identity at runtime — never hardcoded.
type Mailer interface {
	PrimaryIdentity(ctx context.Context) (jmap.Identity, error)
	SendEmail(ctx context.Context, msg jmap.OutgoingMessage) error
}

// Digester builds and sends the daily digest.
type Digester struct {
	store Store
	mail  Mailer
	now   func() time.Time
}

// New returns a Digester.
func New(s Store, m Mailer) *Digester {
	return &Digester{store: s, mail: m, now: time.Now}
}

// WithClock overrides the time source (used in tests).
func (d *Digester) WithClock(now func() time.Time) *Digester {
	d.now = now
	return d
}

// Send composes the digest over the last 24h and emails it to the account
// owner. It satisfies schedule.Digester.
func (d *Digester) Send(ctx context.Context) error {
	now := d.now()
	cutoff := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)

	decisions, err := d.store.DecisionsSince(cutoff)
	if err != nil {
		return err
	}
	errs, err := d.store.ActiveErrors(20)
	if err != nil {
		return err
	}

	subject, body := Compose(decisions, errs, now)

	ident, err := d.mail.PrimaryIdentity(ctx)
	if err != nil {
		return err
	}
	return d.mail.SendEmail(ctx, jmap.OutgoingMessage{
		FromIdentity: ident,
		To:           []string{ident.Email},
		Subject:      subject,
		Text:         body,
	})
}
