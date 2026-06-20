package schedule

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/config"
	"winnow/internal/jmap"
	"winnow/internal/store"
)

func sweepSetup(t *testing.T) (*store.Store, *fakeJMAP, config.Settings) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	defaults := config.Settings{
		DryRun: false, Timezone: "UTC", PollInterval: 15 * time.Minute,
		ConfidenceThreshold: 0.75, LLMDailyCap: 1000, Model: "m", Privacy: config.PrivacySnippet,
	}
	_ = st.SeedSettings(defaults)
	_ = st.SeedCategories()
	fj := &fakeJMAP{
		inbox: jmap.Mailbox{ID: "inbox", Role: "inbox"},
		state: "s1",
		emails: map[string]jmap.Email{
			"e1": {ID: "e1", MailboxIDs: map[string]bool{"inbox": true}, From: []jmap.EmailAddress{{Email: "a@shop.example"}}, Subject: "deal"},
		},
	}
	return st, fj, defaults
}

func newSched(t *testing.T, st *store.Store, fj *fakeJMAP, defaults config.Settings, body string) *Scheduler {
	cl := classify.New(anthropicStub(t, body), st)
	return New(Deps{
		Store: st, Mail: fj, Classifier: cl, Applier: actions.NewApplier(fj),
		Defaults: defaults, Logger: slog.New(slog.NewTextHandler(nopWriter{}, nil)),
	})
}

func TestSweepPreviewDoesNotApply(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	body := `[{"i":0,"category":"Promotional","confidence":0.95,"summary":"deal"}]`
	s := newSched(t, st, fj, defaults, body)

	res, err := s.Sweep(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Considered != 1 {
		t.Errorf("considered = %d", res.Considered)
	}
	if len(fj.updated) != 0 {
		t.Error("preview sweep must not mutate mail")
	}
	if seen, _ := st.IsProcessed("e1"); seen {
		t.Error("preview must not mark processed")
	}
}

func TestSweepApplyMovesAndMarks(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	body := `[{"i":0,"category":"Promotional","confidence":0.95,"summary":"deal"}]`
	s := newSched(t, st, fj, defaults, body)

	if _, err := s.Sweep(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(fj.updated) != 1 || !fj.updated[0].MailboxIDs["mb-Promotions"] {
		t.Errorf("apply sweep should move e1: %+v", fj.updated)
	}
	if seen, _ := st.IsProcessed("e1"); !seen {
		t.Error("apply sweep should mark processed")
	}
}

func TestChangedSinceFallback(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	// Seed a state token so triage runs the changes path (not bootstrap), then
	// force cannotCalculateChanges so it falls back to QueryInbox.
	_ = st.SetEmailState("old-state")
	fj.changesErr = &jmap.MethodError{Type: "cannotCalculateChanges"}
	body := `[{"i":0,"category":"Promotional","confidence":0.95,"summary":"deal"}]`
	s := newSched(t, st, fj, defaults, body)

	s.TriageOnce(context.Background())
	if len(fj.updated) != 1 {
		t.Errorf("fallback path should still process via QueryInbox: %+v", fj.updated)
	}
}
