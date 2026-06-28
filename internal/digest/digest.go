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
	newsletterCategory = "Newsletters"
	maxNewsletters     = 12   // cap summaries per briefing (cost control)
	maxBodyBytes       = 6000 // truncate each body before summarizing
	summarizeMaxTokens = 1024
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
	NewsletterConfig() (on bool, model string, err error) // opt-in summaries toggle + model
}

// Mailer sends the digest via JMAP. The recipient is derived from the account
// identity at runtime — never hardcoded.
type Mailer interface {
	PrimaryIdentity(ctx context.Context) (jmap.Identity, error)
	SendEmail(ctx context.Context, msg jmap.OutgoingMessage) error
}

// Summarizer batch-summarizes newsletter bodies (the opt-in Phase B section).
type Summarizer interface {
	SummarizeNewsletters(ctx context.Context, model string, items []classify.NewsletterInput, maxTokens int) ([]string, error)
}

// BodyFetcher fetches email body text (truncated) for summarization.
type BodyFetcher interface {
	FetchTextBodies(ctx context.Context, ids []string, maxBytes int) (map[string]string, error)
}

// Digester builds and sends the daily digest.
type Digester struct {
	store      Store
	mail       Mailer
	summarizer Summarizer  // optional; nil disables newsletter summaries
	fetcher    BodyFetcher // optional; nil disables newsletter summaries
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
func (d *Digester) WithSummaries(s Summarizer, f BodyFetcher) *Digester {
	d.summarizer = s
	d.fetcher = f
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
	highlights := d.newsletterHighlights(ctx, decisions)

	subject, htmlBody, textBody := ComposeHTML(BriefingData{
		Decisions: decisions, Errors: errs, Proposals: proposals,
		Unsubs: unsubs, Highlights: highlights, LLMToday: llm, Since: since, Now: now,
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

// newsletterHighlights builds the opt-in newsletter summaries. It only runs when
// a summarizer + fetcher are wired AND the toggle is on; it is scoped to the
// Newsletters category, capped, and best-effort — any failure simply omits the
// section so the briefing still sends.
func (d *Digester) newsletterHighlights(ctx context.Context, decisions []store.Decision) []NewsletterHighlight {
	if d.summarizer == nil || d.fetcher == nil {
		return nil
	}
	on, model, err := d.store.NewsletterConfig()
	if err != nil || !on {
		return nil
	}

	// Collect distinct Newsletters-category emails from the window, newest-first,
	// capped.
	type meta struct{ sender, subject string }
	var ids []string
	byID := map[string]meta{}
	for _, dec := range decisions {
		if dec.Category != newsletterCategory || dec.EmailID == "" {
			continue
		}
		if _, seen := byID[dec.EmailID]; seen {
			continue
		}
		byID[dec.EmailID] = meta{sender: dec.Sender, subject: firstNonEmpty(dec.Subject, dec.Sender)}
		ids = append(ids, dec.EmailID)
		if len(ids) >= maxNewsletters {
			break
		}
	}
	if len(ids) == 0 {
		return nil
	}

	bodies, err := d.fetcher.FetchTextBodies(ctx, ids, maxBodyBytes)
	if err != nil {
		return nil
	}
	var inputs []classify.NewsletterInput
	var order []string
	for _, id := range ids {
		body := bodies[id]
		if body == "" {
			continue
		}
		m := byID[id]
		inputs = append(inputs, classify.NewsletterInput{Sender: m.sender, Subject: m.subject, Body: body})
		order = append(order, id)
	}
	if len(inputs) == 0 {
		return nil
	}

	summaries, err := d.summarizer.SummarizeNewsletters(ctx, model, inputs, summarizeMaxTokens)
	if err != nil {
		return nil
	}
	var out []NewsletterHighlight
	for i, id := range order {
		if i >= len(summaries) || summaries[i] == "" {
			continue
		}
		m := byID[id]
		out = append(out, NewsletterHighlight{Sender: m.sender, Subject: m.subject, Summary: summaries[i]})
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
