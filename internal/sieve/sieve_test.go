package sieve

import (
	"context"
	"strings"
	"testing"

	"winnow/internal/jmap"
	"winnow/internal/store"
)

func TestBuildManagedBlockDeterministic(t *testing.T) {
	rules := []CategoryRule{
		{Category: "Social", Folder: "Social", Domains: []string{"b.com", "a.com"}},
		{Category: "Promotional", Folder: "Promotions", Domains: []string{"z.com"}},
	}
	got := BuildManagedBlock(rules)
	if !strings.HasPrefix(got, StartMarker) || !strings.HasSuffix(got, EndMarker) {
		t.Fatalf("block missing markers:\n%s", got)
	}
	// Categories and domains sorted; Promotional before Social.
	if strings.Index(got, "Promotional") > strings.Index(got, "Social") {
		t.Error("categories should be sorted")
	}
	if strings.Index(got, `"a.com"`) > strings.Index(got, `"b.com"`) {
		t.Error("domains should be sorted")
	}
	if !strings.Contains(got, `fileinto "Promotions"`) || !strings.Contains(got, "stop;") {
		t.Errorf("missing fileinto/stop:\n%s", got)
	}
	// Idempotent.
	if BuildManagedBlock(rules) != got {
		t.Error("BuildManagedBlock not deterministic")
	}
}

func TestSplicePreservesUserScriptByteForByte(t *testing.T) {
	user := "require [\"fileinto\"];\n\n# my rule\nif header :contains \"subject\" \"urgent\" {\n\tfileinto \"Urgent\";\n}\n"
	block := BuildManagedBlock([]CategoryRule{{Category: "Promotional", Folder: "Promotions", Domains: []string{"x.com"}}})

	spliced := Splice(user, block)
	// Every byte of the user's script must still be present, in order.
	if !strings.Contains(spliced, user) {
		t.Fatalf("user script not preserved verbatim:\n--- user ---\n%q\n--- spliced ---\n%q", user, spliced)
	}
	if !strings.Contains(spliced, block) {
		t.Fatal("managed block not present after splice")
	}

	// Re-splicing with a new block must replace ONLY the managed region and
	// still preserve the user script verbatim.
	block2 := BuildManagedBlock([]CategoryRule{{Category: "Social", Folder: "Social", Domains: []string{"y.com"}}})
	spliced2 := Splice(spliced, block2)
	if !strings.Contains(spliced2, user) {
		t.Fatal("user script not preserved on re-splice")
	}
	if strings.Contains(spliced2, "x.com") {
		t.Error("old managed block not replaced")
	}
	if !strings.Contains(spliced2, "y.com") {
		t.Error("new managed block not present")
	}
	if strings.Count(spliced2, StartMarker) != 1 {
		t.Errorf("expected exactly one managed block, got %d", strings.Count(spliced2, StartMarker))
	}
}

func TestSpliceEmptyScript(t *testing.T) {
	block := BuildManagedBlock([]CategoryRule{{Category: "Promotional", Folder: "Promotions", Domains: []string{"x.com"}}})
	out := Splice("", block)
	if !strings.Contains(out, block) {
		t.Error("block should be present in empty-script splice")
	}
}

// --- Apply orchestration with fakes ------------------------------------------

type fakeSieveJMAP struct {
	content     string
	exists      bool
	validateErr error
	put         string
	putID       string
}

func (f *fakeSieveJMAP) ActiveSieveScript(context.Context) (jmap.SieveScript, string, bool, error) {
	return jmap.SieveScript{ID: "scr1", Name: "user", IsActive: true}, f.content, f.exists, nil
}
func (f *fakeSieveJMAP) ValidateSieve(_ context.Context, content string) error {
	return f.validateErr
}
func (f *fakeSieveJMAP) PutActiveSieve(_ context.Context, name, content, existingID string) (string, error) {
	f.put = content
	f.putID = existingID
	return "scr1", nil
}

func TestApplyValidatesBeforeActivating(t *testing.T) {
	st := newStoreForSieve(t)
	// Approve a candidate.
	if err := st.ObserveSieveCandidate("retailer.com", "Promotional"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSieveCandidateStatus("retailer.com", "Promotional", store.SieveApproved); err != nil {
		t.Fatal(err)
	}

	// Invalid script: must NOT activate, must NOT back up.
	fj := &fakeSieveJMAP{content: "require [\"fileinto\"];\n", exists: true, validateErr: errBoom}
	g := New(fj, st)
	if err := g.Apply(context.Background()); err == nil {
		t.Fatal("expected error on invalid script")
	}
	if fj.put != "" {
		t.Error("must not activate an invalid script")
	}

	// Valid script: activates and backs up the prior content.
	fj.validateErr = nil
	if err := g.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fj.put, "retailer.com") || !strings.Contains(fj.put, StartMarker) {
		t.Errorf("activated script missing managed block:\n%s", fj.put)
	}
	if backup, ok, _ := st.LatestSieveBackup(); !ok || !strings.Contains(backup, "require") {
		t.Error("prior script should be backed up before activation")
	}
}

func TestRevertRestoresBackup(t *testing.T) {
	st := newStoreForSieve(t)
	if err := st.BackupSieve("require [\"fileinto\"];\n# original\n"); err != nil {
		t.Fatal(err)
	}
	fj := &fakeSieveJMAP{content: "modified", exists: true}
	g := New(fj, st)
	if err := g.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fj.put, "# original") {
		t.Errorf("revert should restore the backup, got %q", fj.put)
	}
}

func newStoreForSieve(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SeedCategories(); err != nil {
		t.Fatal(err)
	}
	return s
}

var errBoom = errBoomType("boom")

type errBoomType string

func (e errBoomType) Error() string { return string(e) }
