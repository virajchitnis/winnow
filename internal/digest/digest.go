// Package digest composes and sends the once-a-day summary email. Its arrival
// also serves as a heartbeat: if it stops coming, something is wrong.
package digest

import (
	"context"
	"time"

	"winnow/internal/classify"
	"winnow/internal/jmap"
	"winnow/internal/store"
)

const (
	maxNewsletters   = 12   // cap newsletters fed per briefing (cost control)
	maxBodyBytes     = 7000 // truncate each body before sending to the model
	composeMaxTokens = 2048 // room for a richer, synthesized briefing
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
	NewsletterConfig() (on bool, model, folder string, err error) // opt-in summaries: toggle, model, source folder
}

// Mailer sends the digest via JMAP. The recipient is derived from the account
// identity at runtime — never hardcoded.
type Mailer interface {
	PrimaryIdentity(ctx context.Context) (jmap.Identity, error)
	SendEmail(ctx context.Context, msg jmap.OutgoingMessage) error
}

// Summarizer composes the newsletters into one synthesized, themed briefing
// (the opt-in newsletter section).
type Summarizer interface {
	ComposeBriefing(ctx context.Context, model string, items []classify.NewsletterInput, maxTokens int) ([]classify.BriefingSection, error)
}

// NewsletterSource reads newsletters straight from their folder — including mail
// moved there server-side that Winnow's inbox triage never processed.
type NewsletterSource interface {
	MailboxByName(ctx context.Context, name string) (jmap.Mailbox, bool, error)
	QueryMailboxSince(ctx context.Context, mailboxID string, after time.Time, limit int) ([]string, error)
	GetEmails(ctx context.Context, ids []string) ([]jmap.Email, error)
	FetchTextBodies(ctx context.Context, ids []string, maxBytes int) (map[string]string, error)
}

// Digester builds and sends the daily digest.
type Digester struct {
	store      Store
	mail       Mailer
	summarizer Summarizer       // optional; nil disables newsletter summaries
	source     NewsletterSource // optional; nil disables newsletter summaries
	now        func() time.Time
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

// WithSummaries enables the opt-in newsletter content summaries. Without it (or
// with the setting off) the briefing simply omits that section.
func (d *Digester) WithSummaries(s Summarizer, src NewsletterSource) *Digester {
	d.summarizer = s
	d.source = src
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
	sections := d.newsletterDigest(ctx, cutoff)

	subject, htmlBody, textBody := ComposeHTML(BriefingData{
		Decisions: decisions, Errors: errs, Proposals: proposals,
		Unsubs: unsubs, Sections: sections, LLMToday: llm, Since: since, Now: now,
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

// newsletterDigest composes a single synthesized briefing from the newsletters
// received since `since`. It reads the Newsletters folder directly (not Winnow's
// decision log), so it includes mail moved there by Fastmail filters or
// graduated Sieve rules that inbox triage never saw. It only runs when a
// summarizer + source are wired AND the toggle is on; it is capped and
// best-effort — any failure simply omits the section so the briefing still sends.
func (d *Digester) newsletterDigest(ctx context.Context, since time.Time) []classify.BriefingSection {
	if d.summarizer == nil || d.source == nil {
		return nil
	}
	on, model, folder, err := d.store.NewsletterConfig()
	if err != nil || !on || folder == "" {
		return nil
	}
	mb, ok, err := d.source.MailboxByName(ctx, folder)
	if err != nil || !ok {
		return nil
	}
	ids, err := d.source.QueryMailboxSince(ctx, mb.ID, since, maxNewsletters)
	if err != nil || len(ids) == 0 {
		return nil
	}

	// Sender/subject give the model context for citations (independent of any
	// Winnow decision).
	type meta struct{ sender, subject string }
	byID := map[string]meta{}
	if emails, e := d.source.GetEmails(ctx, ids); e == nil {
		for _, em := range emails {
			byID[em.ID] = meta{sender: em.SenderEmail(), subject: firstNonEmpty(em.Subject, em.SenderEmail())}
		}
	}

	bodies, err := d.source.FetchTextBodies(ctx, ids, maxBodyBytes)
	if err != nil {
		return nil
	}
	var inputs []classify.NewsletterInput
	for _, id := range ids {
		body := bodies[id]
		if body == "" {
			continue
		}
		m := byID[id]
		inputs = append(inputs, classify.NewsletterInput{Sender: m.sender, Subject: m.subject, Body: body})
	}
	if len(inputs) == 0 {
		return nil
	}

	sections, err := d.summarizer.ComposeBriefing(ctx, model, inputs, composeMaxTokens)
	if err != nil {
		return nil
	}
	return sections
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
