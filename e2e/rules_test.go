//go:build e2e

package e2e

import (
	"testing"

	"github.com/playwright-community/playwright-go"

	"winnow/internal/store"
)

func ruleRow(page playwright.Page, domain string) playwright.Locator {
	return page.Locator(`tr[data-domain="` + domain + `"]`)
}

func TestRuleApprove(t *testing.T) {
	h := newHarness(t)
	if err := h.store.ObserveSieveCandidate("shop.example", "Promotional"); err != nil {
		t.Fatal(err)
	}
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/rules")

	if err := ruleRow(page, "shop.example").Locator(testid("rule-approve")).Click(); err != nil {
		t.Fatalf("click approve: %v", err)
	}
	// Approved count reflects the decision.
	if err := page.Locator(testid("approved-count")).WaitFor(); err != nil {
		t.Fatal(err)
	}
	approved, _ := h.store.SieveCandidates(store.SieveApproved)
	if len(approved) != 1 || approved[0].Domain != "shop.example" {
		t.Errorf("candidate not approved: %+v", approved)
	}
}

func TestRuleReject(t *testing.T) {
	h := newHarness(t)
	if err := h.store.ObserveSieveCandidate("spam.example", "Promotional"); err != nil {
		t.Fatal(err)
	}
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/rules")

	if err := ruleRow(page, "spam.example").Locator(testid("rule-reject")).Click(); err != nil {
		t.Fatalf("click reject: %v", err)
	}
	// The proposal disappears and nothing ends up approved.
	if err := page.Locator(testid("rules-empty")).WaitFor(); err != nil {
		t.Fatalf("expected no proposals after reject: %v", err)
	}
	if approved, _ := h.store.SieveCandidates(store.SieveApproved); len(approved) != 0 {
		t.Errorf("reject should not approve anything, got %+v", approved)
	}
}
