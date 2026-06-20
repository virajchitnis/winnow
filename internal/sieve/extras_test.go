package sieve

import (
	"context"
	"strings"
	"testing"

	"winnow/internal/store"
)

func TestExtractManagedBlock(t *testing.T) {
	block := BuildManagedBlock([]CategoryRule{{Category: "Promotional", Folder: "Promotions", Domains: []string{"x.com"}}})
	script := "require [\"fileinto\"];\n\n" + block + "\n"
	got := ExtractManagedBlock(script)
	if !strings.HasPrefix(got, StartMarker) || !strings.HasSuffix(got, EndMarker) {
		t.Errorf("extracted = %q", got)
	}
	if ExtractManagedBlock("no markers here") != "" {
		t.Error("should return empty when no block present")
	}
}

func TestPreviewAndSetBudget(t *testing.T) {
	st := newStoreForSieve(t)
	_ = st.ObserveSieveCandidate("a.com", "Promotional")
	_ = st.SetSieveCandidateStatus("a.com", "Promotional", store.SieveApproved)

	g := New(&fakeSieveJMAP{exists: false}, st)
	g.SetBudget(64 * 1024)
	block, err := g.Preview()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "a.com") {
		t.Errorf("preview missing approved domain:\n%s", block)
	}
}

func TestApplyNoChangeIsNoop(t *testing.T) {
	st := newStoreForSieve(t)
	// No approved candidates -> empty managed block; existing script already
	// has that empty block, so Apply should be a no-op (no PutActiveSieve).
	empty := BuildManagedBlock(nil)
	fj := &fakeSieveJMAP{content: empty, exists: true}
	g := New(fj, st)
	if err := g.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fj.put != "" {
		t.Error("apply with no change should not activate")
	}
}
