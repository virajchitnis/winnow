package store

import (
	"path/filepath"
	"testing"
	"time"

	"winnow/internal/config"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	fixed := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	s, err := Open(path, WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func defaults() config.Settings {
	return config.Settings{
		DryRun:              true,
		Timezone:            "UTC",
		DigestHour:          7,
		DigestEnabled:       true,
		PollInterval:        15 * time.Minute,
		ConfidenceThreshold: 0.75,
		LLMDailyCap:         2000,
		Model:               "claude-haiku-4-5",
		Privacy:             config.PrivacySnippet,
	}
}

func TestMigrateAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var ver int
	if err := s.DB().QueryRow("PRAGMA user_version").Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != 1 {
		t.Errorf("user_version = %d, want 1", ver)
	}
	s.Close()

	// Reopening should be a no-op (idempotent migrations).
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2.Close()
}

func TestSettingsSeedLoadOverride(t *testing.T) {
	s := testStore(t)
	if err := s.SeedSettings(defaults()); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSettings(defaults())
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfidenceThreshold != 0.75 || got.Model != "claude-haiku-4-5" {
		t.Errorf("seeded settings wrong: %+v", got)
	}

	// DB overrides the env default.
	if err := s.SetSetting(keyConfidence, "0.9"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LoadSettings(defaults())
	if got.ConfidenceThreshold != 0.9 {
		t.Errorf("override not applied: %v", got.ConfidenceThreshold)
	}

	// Seeding again must not clobber the user's edit.
	if err := s.SeedSettings(defaults()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LoadSettings(defaults())
	if got.ConfidenceThreshold != 0.9 {
		t.Errorf("reseed clobbered user value: %v", got.ConfidenceThreshold)
	}
}

func TestCategoriesSeedAndCRUD(t *testing.T) {
	s := testStore(t)
	if err := s.SeedCategories(); err != nil {
		t.Fatal(err)
	}
	cats, err := s.Categories()
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != len(presetCategories) {
		t.Fatalf("got %d categories, want %d", len(cats), len(presetCategories))
	}
	// First by sort order is Important, kept in inbox + flagged.
	if cats[0].Name != "Important" || !cats[0].KeepInInbox || !cats[0].Flag {
		t.Errorf("preset Important wrong: %+v", cats[0])
	}

	promo, ok, err := s.CategoryByName("Promotional")
	if err != nil || !ok {
		t.Fatalf("Promotional lookup: %v ok=%v", err, ok)
	}
	if !promo.Moves() || promo.DestinationFolder != "Promotions" {
		t.Errorf("Promotional should move to Promotions: %+v", promo)
	}

	// Builtin can't be deleted; custom can.
	if err := s.DeleteCategory(promo.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.CategoryByName("Promotional"); !ok {
		t.Error("builtin category should not be deletable")
	}

	id, err := s.CreateCategory(Category{Name: "Receipts", DestinationFolder: "Receipts"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCategory(id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.CategoryByName("Receipts"); ok {
		t.Error("custom category should be deletable")
	}
}

func TestProcessedIdempotency(t *testing.T) {
	s := testStore(t)
	if seen, _ := s.IsProcessed("e1"); seen {
		t.Fatal("e1 should not be processed yet")
	}
	if err := s.MarkProcessed("e1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkProcessed("e1"); err != nil {
		t.Fatalf("re-marking should be a no-op: %v", err)
	}
	if seen, _ := s.IsProcessed("e1"); !seen {
		t.Error("e1 should be processed")
	}
}

func TestSpendCounter(t *testing.T) {
	s := testStore(t)
	n, err := s.AddLLMCalls(3)
	if err != nil || n != 3 {
		t.Fatalf("AddLLMCalls: %v n=%d", err, n)
	}
	n, _ = s.AddLLMCalls(2)
	if n != 5 {
		t.Errorf("counter = %d, want 5", n)
	}
	today, _ := s.LLMCallsToday()
	if today != 5 {
		t.Errorf("LLMCallsToday = %d, want 5", today)
	}
}

func TestErrorsLifecycle(t *testing.T) {
	s := testStore(t)
	if err := s.RecordError("auth", "token expired"); err != nil {
		t.Fatal(err)
	}
	active, _ := s.ActiveErrors(10)
	if len(active) != 1 || active[0].Kind != "auth" {
		t.Fatalf("active errors = %+v", active)
	}
	if err := s.ResolveErrors("auth"); err != nil {
		t.Fatal(err)
	}
	active, _ = s.ActiveErrors(10)
	if len(active) != 0 {
		t.Errorf("errors should be resolved: %+v", active)
	}
}

func TestBoolSettingsRoundTrip(t *testing.T) {
	s := testStore(t)
	// Persist values that DIFFER from the defaults we later load against, so a
	// parse failure (which silently returns the default) is caught.
	_ = s.SetSetting("dry_run", boolStr(false))
	_ = s.SetSetting("digest_enabled", boolStr(true))
	got, err := s.LoadSettings(config.Settings{DryRun: true, DigestEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if got.DryRun {
		t.Error("dry_run=false must override a true default")
	}
	if !got.DigestEnabled {
		t.Error("digest_enabled=true must override a false default")
	}
	// Legacy "on"/"off" values (written by older builds) must still parse.
	_ = s.SetSetting("dry_run", "on")
	if g, _ := s.LoadSettings(config.Settings{DryRun: false}); !g.DryRun {
		t.Error(`legacy "on" should parse as true`)
	}
}

func TestNewsletterConfig(t *testing.T) {
	s := testStore(t)
	if on, _, _, _ := s.NewsletterConfig(); on {
		t.Error("newsletter summaries should default off")
	}
	_ = s.SetSetting("newsletter_summaries", "true")
	_ = s.SetSetting("model", "claude-sonnet-4-6")
	on, model, folder, err := s.NewsletterConfig()
	if err != nil || !on || model != "claude-sonnet-4-6" {
		t.Errorf("NewsletterConfig = (%v, %q, %q, %v)", on, model, folder, err)
	}
	if folder != "Newsletters" { // default when no Newsletters category exists
		t.Errorf("default folder = %q, want Newsletters", folder)
	}
	// A Newsletters category's destination folder overrides the default.
	_, _ = s.CreateCategory(Category{Name: "Newsletters", DestinationFolder: "Reading"})
	if _, _, f, _ := s.NewsletterConfig(); f != "Reading" {
		t.Errorf("folder from category = %q, want Reading", f)
	}
}

func TestLastDigestAtRoundTrip(t *testing.T) {
	s := testStore(t)
	if v, _ := s.LastDigestAt(); v != "" {
		t.Errorf("default last-digest = %q", v)
	}
	_ = s.SetLastDigestAt("2026-06-22T06:00:00Z")
	if v, _ := s.LastDigestAt(); v != "2026-06-22T06:00:00Z" {
		t.Errorf("last-digest = %q", v)
	}
}

func TestRecordErrorDedupes(t *testing.T) {
	s := testStore(t)
	// The same recurring error must collapse to a single active row.
	for i := 0; i < 5; i++ {
		if err := s.RecordError("triage", "dns boom"); err != nil {
			t.Fatal(err)
		}
	}
	active, _ := s.ActiveErrors(10)
	if len(active) != 1 {
		t.Fatalf("identical errors should collapse to 1, got %d", len(active))
	}
	// A different message is its own row.
	_ = s.RecordError("triage", "other boom")
	if active, _ := s.ActiveErrors(10); len(active) != 2 {
		t.Errorf("distinct errors should be separate, got %d", len(active))
	}
	// After resolving, the same message records fresh again (not suppressed).
	_ = s.ResolveErrors("triage")
	_ = s.RecordError("triage", "dns boom")
	if active, _ := s.ActiveErrors(10); len(active) != 1 {
		t.Errorf("a resolved error should re-appear when it recurs, got %d", len(active))
	}
}

func TestSenderStats(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 3; i++ {
		if err := s.RecordObservation("deals@retailer.com", "retailer.com", "Promotional"); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.RecordObservation("news@retailer.com", "retailer.com", "Promotional")
	n, err := s.DomainCategoryCount("retailer.com", "Promotional")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("domain count = %d, want 4 (3+1)", n)
	}
}

func TestDecisionsLog(t *testing.T) {
	s := testStore(t)
	err := s.RecordDecision(Decision{
		EmailID: "e1", Sender: "x@y.com", Subject: "hi",
		Category: "Promotional", Confidence: 0.9, Action: "moved", UsedLLM: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	recent, err := s.RecentDecisions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].EmailID != "e1" || !recent[0].UsedLLM {
		t.Fatalf("recent decisions = %+v", recent)
	}
}

func TestRecordDecisionSupersedes(t *testing.T) {
	s := testStore(t)
	// Triage files it, then a manual Move & teach re-files it: the email must
	// appear once, showing the latest decision — not two rows.
	_ = s.RecordDecision(Decision{EmailID: "e1", Category: "Promotional", Action: "moved", Confidence: 0.9})
	_ = s.RecordDecision(Decision{EmailID: "e1", Category: "Important", Action: "flagged", Confidence: 1, Reason: "manual re-file"})

	recent, _ := s.RecentDecisions(10)
	if len(recent) != 1 {
		t.Fatalf("want 1 row per email, got %d: %+v", len(recent), recent)
	}
	if recent[0].Category != "Important" || recent[0].Action != "flagged" {
		t.Errorf("latest decision should win: %+v", recent[0])
	}
	// A transient error does not clobber the good decision.
	_ = s.RecordDecision(Decision{EmailID: "e1", Action: "error"})
	if r, _ := s.RecentDecisions(10); len(r) != 2 {
		t.Errorf("error should be recorded alongside the good row, got %d", len(r))
	}
	// A later success collapses back to one row.
	_ = s.RecordDecision(Decision{EmailID: "e1", Category: "Important", Action: "flagged"})
	if r, _ := s.RecentDecisions(10); len(r) != 1 {
		t.Errorf("success should supersede prior rows, got %d", len(r))
	}
}

func TestQueryDecisions(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 5; i++ {
		if err := s.RecordDecision(Decision{
			EmailID: "e" + string(rune('1'+i)), Action: "moved", Category: "Promotional",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Default date sort, newest-first, paged.
	page, err := s.QueryDecisions(DecisionQuery{Sort: "date", Desc: true, Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].EmailID != "e5" {
		t.Fatalf("page 0 = %+v", page)
	}
	if p2, _ := s.QueryDecisions(DecisionQuery{Sort: "date", Desc: true, Limit: 2, Offset: 2}); len(p2) != 2 || p2[0].EmailID != "e3" {
		t.Fatalf("page 1 = %+v", p2)
	}

	// Confidence sort ascending orders by the confidence column.
	_ = s.RecordDecision(Decision{EmailID: "hi", Category: "Promotional", Action: "moved", Confidence: 0.9})
	_ = s.RecordDecision(Decision{EmailID: "lo", Category: "Promotional", Action: "moved", Confidence: 0.1})
	asc, _ := s.QueryDecisions(DecisionQuery{Sort: "confidence", Desc: false, Limit: 100})
	if asc[0].Confidence > asc[len(asc)-1].Confidence {
		t.Errorf("confidence asc not ordered: %v..%v", asc[0].Confidence, asc[len(asc)-1].Confidence)
	}

	// Search matches subject/sender/category (case-insensitive).
	_ = s.RecordDecision(Decision{EmailID: "s1", Sender: "deals@shop.com", Subject: "Big SALE today", Category: "Promotional", Action: "moved"})
	hits, _ := s.QueryDecisions(DecisionQuery{Search: "sale", Limit: 100})
	if len(hits) != 1 || hits[0].EmailID != "s1" {
		t.Fatalf("search 'sale' = %+v", hits)
	}
	if got, _ := s.QueryDecisions(DecisionQuery{Search: "shop.com", Limit: 100}); len(got) != 1 {
		t.Errorf("search by sender should match, got %d", len(got))
	}

	// Unknown sort key falls back to date (no error, returns rows).
	if rows, err := s.QueryDecisions(DecisionQuery{Sort: "bogus", Limit: 100}); err != nil || len(rows) == 0 {
		t.Fatalf("fallback sort failed: %v rows=%d", err, len(rows))
	}
}
