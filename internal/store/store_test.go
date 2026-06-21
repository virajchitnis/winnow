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

func TestDecisionsPage(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 5; i++ {
		if err := s.RecordDecision(Decision{
			EmailID: "e" + string(rune('1'+i)), Action: "moved", Category: "Promotional",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Newest-first: first page of 2 starts at the last inserted (e5).
	page, err := s.DecisionsPage(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].EmailID != "e5" {
		t.Fatalf("page 0 = %+v", page)
	}
	// Offset 2 returns the next slice.
	page2, _ := s.DecisionsPage(2, 2)
	if len(page2) != 2 || page2[0].EmailID != "e3" {
		t.Fatalf("page 1 = %+v", page2)
	}
	// Offset past the end is empty.
	if p, _ := s.DecisionsPage(2, 10); len(p) != 0 {
		t.Fatalf("expected empty tail page, got %+v", p)
	}
}
