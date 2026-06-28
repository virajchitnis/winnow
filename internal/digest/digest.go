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
	SieveCandidates(status string) ([]store.SieveCandidate, error)
	UnsubscribeCandidates(status string) ([]store.UnsubscribeRecord, error)
	LLMCallsToday() (int, error)
	LastDigestAt() (string, error)
	SetLastDigestAt(ts string) error
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

// Send composes the morning briefing covering everything since the last send
// (falling back to 24h) and emails it (HTML + text) to the account owner. On a
// successful send it advances the last-sent watermark. Satisfies
// schedule.Digester.
func (d *Digester) Send(ctx context.Context) error {
	now := d.now()

	// Window: since the last briefing, else the last 24h.
	var since time.Time
	if last, _ := d.store.LastDigestAt(); last != "" {
		if t, err := time.Parse(time.RFC3339Nano, last); err == nil && t.Before(now) {
			since = t
		}
	}
	cutoff := now.Add(-24 * time.Hour)
	if !since.IsZero() {
		cutoff = since
	}

	decisions, err := d.store.DecisionsSince(cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	errs, err := d.store.ActiveErrors(20)
	if err != nil {
		return err
	}
	proposals, _ := d.store.SieveCandidates(store.SieveProposed)
	unsubs, _ := d.store.UnsubscribeCandidates(store.UnsubNeedsDecision)
	llm, _ := d.store.LLMCallsToday()

	subject, htmlBody, textBody := ComposeHTML(BriefingData{
		Decisions: decisions, Errors: errs, Proposals: proposals,
		Unsubs: unsubs, LLMToday: llm, Since: since, Now: now,
	})

	ident, err := d.mail.PrimaryIdentity(ctx)
	if err != nil {
		return err
	}
	if err := d.mail.SendEmail(ctx, jmap.OutgoingMessage{
		FromIdentity: ident,
		To:           []string{ident.Email},
		Subject:      subject,
		Text:         textBody,
		HTML:         htmlBody,
	}); err != nil {
		return err
	}
	return d.store.SetLastDigestAt(now.UTC().Format(time.RFC3339Nano))
}
