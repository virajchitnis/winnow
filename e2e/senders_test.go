//go:build e2e

package e2e

import (
	"testing"

	"github.com/playwright-community/playwright-go"
)

func TestSenderDenyBulkOverride(t *testing.T) {
	h := newHarness(t)
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/senders")

	mustFill(t, page, "sender-pattern", "@bank.example")
	if _, err := page.Locator(testid("sender-kind")).SelectOption(
		playwright.SelectOptionValues{Values: &[]string{"deny_bulk"}}); err != nil {
		t.Fatal(err)
	}
	mustFill(t, page, "sender-category", "Promotional")
	mustClick(t, page, "sender-add")

	if err := page.Locator(`tr[data-pattern="@bank.example"]`).WaitFor(); err != nil {
		t.Fatalf("rule row not shown: %v", err)
	}
	// The rule is a hard override: any sender at the domain resolves to it.
	cat, important, ok := h.store.SenderOverride("anyone@bank.example", "bank.example")
	if !ok || important || cat != "Promotional" {
		t.Errorf("deny-bulk override = (%q, important=%v, ok=%v), want Promotional", cat, important, ok)
	}

	// Remove it.
	if err := page.Locator(`tr[data-pattern="@bank.example"]`).Locator(testid("sender-remove")).Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("senders-empty")).WaitFor(); err != nil {
		t.Fatalf("expected empty senders list after remove: %v", err)
	}
	if _, _, ok := h.store.SenderOverride("anyone@bank.example", "bank.example"); ok {
		t.Error("override should be gone after remove")
	}
}

func TestSenderAllowImportantOverride(t *testing.T) {
	h := newHarness(t)
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/senders")

	mustFill(t, page, "sender-pattern", "vip@corp.example")
	if _, err := page.Locator(testid("sender-kind")).SelectOption(
		playwright.SelectOptionValues{Values: &[]string{"allow_important"}}); err != nil {
		t.Fatal(err)
	}
	mustClick(t, page, "sender-add")

	if err := page.Locator(`tr[data-pattern="vip@corp.example"]`).WaitFor(); err != nil {
		t.Fatalf("rule row not shown: %v", err)
	}
	_, important, ok := h.store.SenderOverride("vip@corp.example", "corp.example")
	if !ok || !important {
		t.Errorf("allow-important override = (important=%v, ok=%v), want important", important, ok)
	}
}
