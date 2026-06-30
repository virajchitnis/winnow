package digest

import (
	"context"
	"strings"
	"testing"
	"time"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/jmap"
	"winnow/internal/store"
)

func TestComposeCountsAndSections(t *testing.T) {
	now := time.Date(2026, 6, 18, 7, 0, 0, 0, time.UTC)
	decisions := []store.Decision{
		{Sender: "a@x.com", Category: "Promotional", Action: string(actions.ActionMoved)},
		{Sender: "b@x.com", Category: "Promotional", Action: string(actions.ActionMoved)},
		{Sender: "c@y.com", Category: "Social", Action: string(actions.ActionMoved)},
		{Sender: "boss@work.com", Category: "Important", Action: string(actions.ActionFlagged), Summary: "Q3 numbers"},
		{Sender: "huh@z.com", Category: "Needs attention", Action: string(actions.ActionKept), LowConfidence: true, Subject: "is this you?"},
	}
	errs := []store.AppError{{Kind: "auth", Message: "token expired"}}

	subject, body := Compose(decisions, errs, now)
	if !strings.Contains(subject, "Jun 18") {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{
		"Processed 5 emails",
		"3 filed to folders",
		"1 flagged",
		"Promotional: 2",
		"Flagged for your attention",
		"boss@work.com — Q3 numbers",
		"low confidence",
		"is this you?",
		"token expired",
		"heartbeat",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n---\n%s", want, body)
		}
	}
}

func TestComposeEmpty(t *testing.T) {
	_, body := Compose(nil, nil, time.Now())
	if !strings.Contains(body, "Processed 0 emails") {
		t.Errorf("empty digest wrong: %s", body)
	}
}

// fakes for Send.
type fakeStore struct {
	decisions []store.Decision
	lastSetTo string
	nlOn      bool
}

func (f fakeStore) DecisionsSince(string) ([]store.Decision, error) { return f.decisions, nil }
func (f fakeStore) ActiveErrors(int) ([]store.AppError, error)      { return nil, nil }
func (f fakeStore) SieveCandidates(string) ([]store.SieveCandidate, error) {
	return nil, nil
}
func (f fakeStore) UnsubscribeCandidates(string) ([]store.UnsubscribeRecord, error) {
	return nil, nil
}
func (f fakeStore) LLMCallsToday() (int, error)   { return 0, nil }
func (f fakeStore) LastDigestAt() (string, error) { return "", nil }
func (f fakeStore) NewsletterConfig() (bool, string, string, error) {
	return f.nlOn, "m", "Newsletters", nil
}
func (f *fakeStore) SetLastDigestAt(ts string) error {
	f.lastSetTo = ts
	return nil
}

type fakeMailer struct{ sent *jmap.OutgoingMessage }

func (f *fakeMailer) PrimaryIdentity(context.Context) (jmap.Identity, error) {
	return jmap.Identity{ID: "i", Email: "me@example.com"}, nil
}
func (f *fakeMailer) SendEmail(_ context.Context, m jmap.OutgoingMessage) error {
	f.sent = &m
	return nil
}

func TestSendToSelf(t *testing.T) {
	fm := &fakeMailer{}
	fs := &fakeStore{decisions: []store.Decision{{Sender: "a@b.com", Action: "moved", Category: "Promotional"}}}
	d := New(fs, fm)
	if err := d.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fm.sent == nil || fm.sent.To[0] != "me@example.com" {
		t.Fatalf("digest not sent to self: %+v", fm.sent)
	}
	if !strings.Contains(fm.sent.Subject, "Winnow briefing") {
		t.Errorf("subject = %q", fm.sent.Subject)
	}
	if fm.sent.HTML == "" || !strings.Contains(fm.sent.HTML, "Morning briefing") {
		t.Errorf("expected an HTML briefing body")
	}
	if fs.lastSetTo == "" {
		t.Error("Send should advance the last-digest watermark")
	}
}

type fakeSummarizer struct{ out []string }

func (f fakeSummarizer) SummarizeNewsletters(_ context.Context, _ string, _ []classify.NewsletterInput, _ int) ([]string, error) {
	return f.out, nil
}

// fakeSource reads "newsletters" from an in-memory folder.
type fakeSource struct {
	box    jmap.Mailbox
	boxOK  bool
	ids    []string
	emails []jmap.Email
	bodies map[string]string
}

func (f fakeSource) MailboxByName(context.Context, string) (jmap.Mailbox, bool, error) {
	return f.box, f.boxOK, nil
}
func (f fakeSource) QueryMailboxSince(context.Context, string, time.Time, int) ([]string, error) {
	return f.ids, nil
}
func (f fakeSource) GetEmails(context.Context, []string) ([]jmap.Email, error) {
	return f.emails, nil
}
func (f fakeSource) FetchTextBodies(context.Context, []string, int) (map[string]string, error) {
	return f.bodies, nil
}

func newsletterSource() fakeSource {
	return fakeSource{
		box: jmap.Mailbox{ID: "mb-news", Name: "Newsletters"}, boxOK: true,
		ids:    []string{"n1"},
		emails: []jmap.Email{{ID: "n1", From: []jmap.EmailAddress{{Email: "weekly@news.com"}}, Subject: "This week in tech"}},
		bodies: map[string]string{"n1": "full newsletter body"},
	}
}

func TestNewsletterHighlights(t *testing.T) {
	fm := &fakeMailer{}
	fs := &fakeStore{nlOn: true}
	d := New(fs, fm).WithSummaries(
		fakeSummarizer{out: []string{"Three big stories about chips, AI, and launches."}},
		newsletterSource(),
	)
	if err := d.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Newsletter highlights", "This week in tech", "Three big stories", "weekly@news.com"} {
		if !strings.Contains(fm.sent.HTML, want) {
			t.Errorf("briefing missing %q", want)
		}
	}
}

func TestNewsletterHighlightsOffByDefault(t *testing.T) {
	fm := &fakeMailer{}
	fs := &fakeStore{nlOn: false}
	d := New(fs, fm).WithSummaries(fakeSummarizer{out: []string{"should not appear"}}, newsletterSource())
	if err := d.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fm.sent.HTML, "Newsletter highlights") {
		t.Error("summaries must be off unless the setting is enabled")
	}
}

func TestComposeHTMLSections(t *testing.T) {
	now := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)
	_, html, text := ComposeHTML(BriefingData{
		Decisions: []store.Decision{
			{Sender: "deals@shop.com", Action: "moved", Category: "Promotional"},
			{Sender: "boss@work.com", Subject: "Q3 plan", Action: "flagged", Category: "Important"},
			{Sender: "maybe@x.com", Action: "kept", Category: "Newsletters", LowConfidence: true},
		},
		Proposals: []store.SieveCandidate{{Domain: "shop.com", Category: "Promotional", Observations: 9}},
		Unsubs:    []store.UnsubscribeRecord{{Sender: "spam@x.com", Count: 12}},
		LLMToday:  3,
		Now:       now,
	})
	for _, want := range []string{
		"Needs your attention", "boss@work.com",
		"Waiting for your approval", "@shop.com", "spam@x.com",
		"Filed by category", "Busiest senders",
		"worth a look", "Cost &amp; health", "3 Claude calls",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML briefing missing %q", want)
		}
	}
	if text == "" {
		t.Error("expected a plain-text fallback")
	}
}
