package schedule

import (
	"testing"

	"winnow/internal/classify"
	"winnow/internal/store"
)

func TestPlanForMovingCategory(t *testing.T) {
	cat := store.Category{Name: "Promotional", DestinationFolder: "Promotions", MarkRead: true}
	r := classify.Result{Category: "Promotional", Confidence: 0.9, Source: classify.SourceLLM, UsedLLM: true}
	p, low := planFor(r, cat, 0.75)
	if low {
		t.Fatal("0.9 >= 0.75 should not be low confidence")
	}
	if p.MoveTo != "Promotions" || !p.MarkRead || p.Flag {
		t.Errorf("plan = %+v", p)
	}
}

func TestPlanForKeepCategoryFlagged(t *testing.T) {
	cat := store.Category{Name: "Important", KeepInInbox: true, Flag: true}
	r := classify.Result{Category: "Important", Confidence: 1, Source: classify.SourceAllow}
	p, low := planFor(r, cat, 0.75)
	if low || p.MoveTo != "" || !p.Flag {
		t.Errorf("important should stay in inbox, flagged, not low: plan=%+v low=%v", p, low)
	}
}

func TestPlanForLowConfidenceKeeps(t *testing.T) {
	cat := store.Category{Name: "Promotional", DestinationFolder: "Promotions"}
	r := classify.Result{Category: "Promotional", Confidence: 0.4, Source: classify.SourceLLM, UsedLLM: true}
	p, low := planFor(r, cat, 0.75)
	if !low {
		t.Fatal("0.4 < 0.75 should be low confidence")
	}
	if p.MoveTo != "" || p.Flag {
		t.Errorf("low-confidence mail must stay in inbox, unflagged: %+v", p)
	}
}

func TestPlanForKnownSenderNotGated(t *testing.T) {
	// A known sender carries 0.95 confidence; even with a high threshold it is
	// not gated because the gate only applies to LLM/fallback sources.
	cat := store.Category{Name: "Promotional", DestinationFolder: "Promotions"}
	r := classify.Result{Category: "Promotional", Confidence: 0.95, Source: classify.SourceKnown}
	p, low := planFor(r, cat, 0.99)
	if low {
		t.Fatal("known-sender results must not be confidence-gated")
	}
	if p.MoveTo != "Promotions" {
		t.Errorf("known sender should move: %+v", p)
	}
}

func TestDomainOf(t *testing.T) {
	cases := map[string]string{
		"deals@retailer.com": "retailer.com",
		"noatsign":           "",
		"trailing@":          "",
		"":                   "",
	}
	for in, want := range cases {
		if got := domainOf(in); got != want {
			t.Errorf("domainOf(%q) = %q, want %q", in, got, want)
		}
	}
}
