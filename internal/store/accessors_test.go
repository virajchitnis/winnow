package store

import (
	"testing"
	"time"
)

func TestSenderRulesAndOverride(t *testing.T) {
	s := testStore(t)
	if err := s.AddSenderRule("boss@work.com", KindAllowImportant, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSenderRule("@spam.com", KindDenyBulk, "Promotional"); err != nil {
		t.Fatal(err)
	}
	rules, _ := s.SenderRules()
	if len(rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rules))
	}

	if _, important, ok := s.SenderOverride("boss@work.com", "work.com"); !ok || !important {
		t.Error("allow override not matched")
	}
	if cat, important, ok := s.SenderOverride("x@spam.com", "spam.com"); !ok || important || cat != "Promotional" {
		t.Errorf("deny override = %q,%v,%v", cat, important, ok)
	}
	if _, _, ok := s.SenderOverride("nobody@nowhere.com", "nowhere.com"); ok {
		t.Error("unexpected match")
	}

	if err := s.DeleteSenderRule("boss@work.com", KindAllowImportant); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.SenderOverride("boss@work.com", "work.com"); ok {
		t.Error("rule should be deleted")
	}
}

func TestKnownCategoryDominance(t *testing.T) {
	s := testStore(t)
	// 5 observations, all Promotional for a domain -> known.
	for i := 0; i < 5; i++ {
		_ = s.RecordObservation("a@shop.com", "shop.com", "Promotional")
	}
	if cat, ok := s.KnownCategory("a@shop.com", "shop.com"); !ok || cat != "Promotional" {
		t.Errorf("KnownCategory = %q,%v", cat, ok)
	}
	// Too few observations -> not known.
	_ = s.RecordObservation("b@new.com", "new.com", "Social")
	if _, ok := s.KnownCategory("b@new.com", "new.com"); ok {
		t.Error("single observation should not be known")
	}
	// Approved sieve candidate counts as known for the domain.
	_ = s.ObserveSieveCandidate("rules.com", "Newsletters")
	_ = s.SetSieveCandidateStatus("rules.com", "Newsletters", SieveApproved)
	if cat, ok := s.KnownCategory("z@rules.com", "rules.com"); !ok || cat != "Newsletters" {
		t.Errorf("approved candidate should be known: %q,%v", cat, ok)
	}
}

func TestSieveCandidatesLifecycle(t *testing.T) {
	s := testStore(t)
	_ = s.ObserveSieveCandidate("a.com", "Promotional")
	_ = s.ObserveSieveCandidate("a.com", "Promotional")
	cands, _ := s.SieveCandidates(SieveProposed)
	if len(cands) != 1 || cands[0].Observations != 2 {
		t.Fatalf("candidates = %+v", cands)
	}
	_ = s.SetSieveCandidateStatus("a.com", "Promotional", SieveApproved)
	if got, _ := s.SieveCandidates(SieveApproved); len(got) != 1 {
		t.Errorf("approved candidates = %d", len(got))
	}
	if got, _ := s.SieveCandidates(SieveProposed); len(got) != 0 {
		t.Errorf("should be no proposed left: %d", len(got))
	}
}

func TestSieveBackups(t *testing.T) {
	s := testStore(t)
	if _, ok, _ := s.LatestSieveBackup(); ok {
		t.Error("no backup expected initially")
	}
	_ = s.BackupSieve("v1")
	_ = s.BackupSieve("v2")
	got, ok, _ := s.LatestSieveBackup()
	if !ok || got != "v2" {
		t.Errorf("latest backup = %q,%v", got, ok)
	}
}

func TestUnsubscribeLifecycle(t *testing.T) {
	s := testStore(t)
	_ = s.ObserveUnsubscribe("a@x.com", UnsubMethodOneClick, "https://x.com/u")
	_ = s.ObserveUnsubscribe("a@x.com", UnsubMethodOneClick, "https://x.com/u")
	rec, ok, _ := s.UnsubscribeRecordBySender("a@x.com")
	if !ok || rec.Count != 2 || rec.Status != UnsubNeedsDecision {
		t.Fatalf("record = %+v ok=%v", rec, ok)
	}
	if err := s.SetUnsubscribeStatus("a@x.com", UnsubUnsubscribed, true); err != nil {
		t.Fatal(err)
	}
	rec, _, _ = s.UnsubscribeRecordBySender("a@x.com")
	if rec.Status != UnsubUnsubscribed || rec.ActedAt == "" {
		t.Errorf("after unsub: %+v", rec)
	}
	_ = s.SetUnsubscribeVerified("a@x.com", false)
	if got, _ := s.UnsubscribeCandidates(UnsubUnsubscribed); len(got) != 1 {
		t.Errorf("filter unsubscribed = %d", len(got))
	}
}

func TestDecisionsSinceAndPrune(t *testing.T) {
	s := testStore(t) // clock fixed at 2026-06-18 12:00 UTC
	_ = s.RecordDecision(Decision{EmailID: "e1", Action: "moved", Category: "Promotional"})

	future := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if got, _ := s.DecisionsSince(future); len(got) != 0 {
		t.Errorf("nothing should be after the future cutoff: %d", len(got))
	}
	past := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if got, _ := s.DecisionsSince(past); len(got) != 1 {
		t.Errorf("decision should be after past cutoff: %d", len(got))
	}
	n, _ := s.PruneDecisions(future)
	if n != 1 {
		t.Errorf("prune removed %d, want 1", n)
	}
}

func TestEmailStateRoundTrip(t *testing.T) {
	s := testStore(t)
	if v, _ := s.EmailState(); v != "" {
		t.Errorf("initial state = %q", v)
	}
	_ = s.SetEmailState("tok-123")
	if v, _ := s.EmailState(); v != "tok-123" {
		t.Errorf("state = %q", v)
	}
}
