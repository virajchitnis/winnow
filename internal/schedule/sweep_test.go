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

func TestSweepPreviewIsSideEffectFree(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	body := `[{"i":0,"category":"Promotional","confidence":0.95,"summary":"deal"}]`
	s := newSched(t, st, fj, defaults, body)

	// Run the preview twice — it must be re-runnable cleanly.
	for i := 0; i < 2; i++ {
		if _, err := s.Sweep(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}

	// Exactly one decision row despite two previews (no duplicates).
	decisions, _ := st.RecentDecisions(10)
	if len(decisions) != 1 {
		t.Errorf("preview should keep one decision per email, got %d", len(decisions))
	}
	// No learning side-effects: sender stats and Sieve candidates untouched.
	if n, _ := st.DomainCategoryCount("shop.example", "Promotional"); n != 0 {
		t.Errorf("preview must not record sender observations, got count %d", n)
	}
	if cands, _ := st.SieveCandidates("proposed"); len(cands) != 0 {
		t.Errorf("preview must not propose Sieve candidates, got %d", len(cands))
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
	// Apply (unlike preview) seeds the rule-learning stats.
	if n, _ := st.DomainCategoryCount("shop.example", "Promotional"); n != 1 {
		t.Errorf("apply sweep should record one observation, got %d", n)
	}
}

func TestRefileMovesAndMarks(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	s := newSched(t, st, fj, defaults, "")

	action, err := s.Refile(context.Background(), "e1", "Promotional")
	if err != nil {
		t.Fatal(err)
	}
	if action != "moved" {
		t.Errorf("action = %q, want moved", action)
	}
	if len(fj.updated) != 1 || !fj.updated[0].MailboxIDs["mb-Promotions"] {
		t.Errorf("refile should move e1 to Promotions: %+v", fj.updated)
	}
	if seen, _ := st.IsProcessed("e1"); !seen {
		t.Error("refile should mark processed")
	}
}

func TestRefileToKeepInInboxMovesBackAndFlags(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	fj.inbox = jmap.Mailbox{ID: "inbox", Role: "inbox", Name: "Inbox"}
	s := newSched(t, st, fj, defaults, "")

	// "Important" is a keep-in-inbox, flag category: refile returns it to the
	// inbox and sets the flag.
	action, err := s.Refile(context.Background(), "e1", "Important")
	if err != nil {
		t.Fatal(err)
	}
	if len(fj.updated) != 1 {
		t.Fatalf("expected one update, got %+v", fj.updated)
	}
	if !fj.updated[0].MailboxIDs["mb-Inbox"] {
		t.Errorf("should move back to inbox: %+v", fj.updated[0])
	}
	if !fj.updated[0].SetKeywords["$flagged"] {
		t.Errorf("Important should flag: %+v", fj.updated[0])
	}
	_ = action
}

func TestRefileUnknownCategory(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	s := newSched(t, st, fj, defaults, "")
	if _, err := s.Refile(context.Background(), "e1", "Nope"); err == nil {
		t.Error("expected error for unknown category")
	}
	if len(fj.updated) != 0 {
		t.Error("unknown category must not move mail")
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
