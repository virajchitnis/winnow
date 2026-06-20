package digest

import (
	"context"
	"strings"
	"testing"
	"time"

	"winnow/internal/actions"
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
}

func (f fakeStore) DecisionsSince(string) ([]store.Decision, error) { return f.decisions, nil }
func (f fakeStore) ActiveErrors(int) ([]store.AppError, error)      { return nil, nil }

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
	d := New(fakeStore{decisions: []store.Decision{{Sender: "a@b.com", Action: "moved", Category: "Promotional"}}}, fm)
	if err := d.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fm.sent == nil || fm.sent.To[0] != "me@example.com" {
		t.Fatalf("digest not sent to self: %+v", fm.sent)
	}
	if !strings.Contains(fm.sent.Subject, "Winnow digest") {
		t.Errorf("subject = %q", fm.sent.Subject)
	}
}
