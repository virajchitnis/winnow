package schedule

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/config"
	"winnow/internal/jmap"
	"winnow/internal/store"
)

// fakeJMAP satisfies both schedule.Mailer and actions.JMAP.
type fakeJMAP struct {
	inbox      jmap.Mailbox
	state      string
	changes    *jmap.Changes
	changesErr error
	emails     map[string]jmap.Email
	updated    []jmap.EmailUpdate
}

func (f *fakeJMAP) MailboxByRole(_ context.Context, role string) (jmap.Mailbox, bool, error) {
	if role == "inbox" {
		return f.inbox, true, nil
	}
	return jmap.Mailbox{}, false, nil
}
func (f *fakeJMAP) MailboxState(context.Context) (string, error) { return f.state, nil }
func (f *fakeJMAP) EmailChanges(_ context.Context, _ string, _ int) (*jmap.Changes, error) {
	if f.changesErr != nil {
		return nil, f.changesErr
	}
	return f.changes, nil
}
func (f *fakeJMAP) QueryInbox(_ context.Context, _ string, _ int) ([]string, error) {
	ids := make([]string, 0, len(f.emails))
	for id := range f.emails {
		ids = append(ids, id)
	}
	return ids, nil
}
func (f *fakeJMAP) GetEmails(_ context.Context, ids []string) ([]jmap.Email, error) {
	var out []jmap.Email
	for _, id := range ids {
		if e, ok := f.emails[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeJMAP) EnsureMailbox(_ context.Context, name string) (string, error) {
	return "mb-" + name, nil
}
func (f *fakeJMAP) UpdateEmails(_ context.Context, u []jmap.EmailUpdate) (map[string]string, error) {
	f.updated = append(f.updated, u...)
	return nil, nil
}

func anthropicStub(t *testing.T, body string) *classify.Anthropic {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"content":     []map[string]any{{"type": "text", "text": body}},
		})
	}))
	t.Cleanup(srv.Close)
	return classify.NewAnthropic("k", classify.WithBaseURL(srv.URL))
}

func TestTriageCycleEndToEnd(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	defaults := config.Settings{
		DryRun: false, Timezone: "UTC", DigestHour: 7, DigestEnabled: false,
		PollInterval: 15 * time.Minute, ConfidenceThreshold: 0.75, LLMDailyCap: 1000,
		Model: "claude-haiku-4-5", Privacy: config.PrivacySnippet,
	}
	if err := st.SeedSettings(defaults); err != nil {
		t.Fatal(err)
	}
	if err := st.SeedCategories(); err != nil {
		t.Fatal(err)
	}
	// Seed the state token so triage processes changes (not bootstrap).
	if err := st.SetEmailState("s1"); err != nil {
		t.Fatal(err)
	}

	fj := &fakeJMAP{
		inbox:   jmap.Mailbox{ID: "inbox", Role: "inbox"},
		state:   "s2",
		changes: &jmap.Changes{NewState: "s2", Created: []string{"e1", "e2"}},
		emails: map[string]jmap.Email{
			"e1": {ID: "e1", MailboxIDs: map[string]bool{"inbox": true}, From: []jmap.EmailAddress{{Email: "deals@retailer.example"}}, Subject: "40% off", ListUnsubscribe: "<mailto:u@retailer.example>"},
			"e2": {ID: "e2", MailboxIDs: map[string]bool{"inbox": true}, From: []jmap.EmailAddress{{Email: "unknown@x.example"}}, Subject: "hi"},
		},
	}

	// e1 -> Promotional (high conf, moves); e2 -> low conf (kept in inbox).
	body := `[{"i":0,"category":"Promotional","confidence":0.95,"reason":"sale","summary":"40% off"},
	          {"i":1,"category":"Needs attention","confidence":0.3,"reason":"unclear","summary":"hi"}]`
	cl := classify.New(anthropicStub(t, body), st)
	ap := actions.NewApplier(fj)

	sched := New(Deps{
		Store: st, Mail: fj, Classifier: cl, Applier: ap,
		Defaults: defaults, Logger: slog.New(slog.NewTextHandler(nopWriter{}, nil)),
	})

	sched.TriageOnce(context.Background())

	// State advanced.
	if state, _ := st.EmailState(); state != "s2" {
		t.Errorf("email state = %q, want s2", state)
	}
	// Two decisions recorded.
	decisions, _ := st.RecentDecisions(10)
	if len(decisions) != 2 {
		t.Fatalf("want 2 decisions, got %d", len(decisions))
	}
	// e1 moved to Promotions; e2 kept (only e1 produces a move update).
	moved := false
	for _, u := range fj.updated {
		if u.ID == "e1" && u.MailboxIDs["mb-Promotions"] {
			moved = true
		}
		if u.ID == "e2" && u.MailboxIDs != nil {
			t.Error("e2 (low confidence) must not be moved")
		}
	}
	if !moved {
		t.Errorf("e1 should have been moved; updates=%+v", fj.updated)
	}
	// Idempotency: e1 marked processed.
	if seen, _ := st.IsProcessed("e1"); !seen {
		t.Error("e1 should be marked processed")
	}
	// Health recorded success.
	if h := sched.HealthSnapshot(); !h.LastPollOK {
		t.Error("health should report last poll OK")
	}
	// Unsubscribe metadata persisted for e1.
	if _, ok, _ := st.UnsubscribeRecordBySender("deals@retailer.example"); !ok {
		t.Error("unsubscribe metadata should be persisted for e1")
	}
}

func TestTriageBootstrapsOnFirstRun(t *testing.T) {
	st, _ := store.Open(t.TempDir() + "/s.db")
	defer st.Close()
	defaults := config.Settings{PollInterval: 15 * time.Minute, ConfidenceThreshold: 0.75, LLMDailyCap: 1000, Model: "m", Privacy: config.PrivacySnippet, Timezone: "UTC"}
	_ = st.SeedSettings(defaults)
	_ = st.SeedCategories()

	fj := &fakeJMAP{inbox: jmap.Mailbox{ID: "inbox", Role: "inbox"}, state: "init"}
	sched := New(Deps{Store: st, Mail: fj, Classifier: nil, Applier: nil, Defaults: defaults, Logger: slog.New(slog.NewTextHandler(nopWriter{}, nil))})
	sched.TriageOnce(context.Background())

	if state, _ := st.EmailState(); state != "init" {
		t.Errorf("first run should record current state, got %q", state)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
