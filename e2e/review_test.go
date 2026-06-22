//go:build e2e

package e2e

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	"winnow/internal/store"
)

func seedDecision(t *testing.T, h *harness, d store.Decision) {
	t.Helper()
	if err := h.store.RecordDecision(d); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
}

func TestReviewListAndSearch(t *testing.T) {
	h := newHarness(t)
	seedDecision(t, h, store.Decision{EmailID: "a", Sender: "deals@shop.example", Subject: "Weekly SALE inside", Category: "Promotional", Action: "dry_run", Confidence: 0.9})
	seedDecision(t, h, store.Decision{EmailID: "b", Sender: "boss@work.example", Subject: "Quarterly numbers", Category: "Important", Action: "dry_run", Confidence: 0.8})

	page := newPage(t)
	login(t, page, h.ts.URL)

	if n, _ := page.Locator(testid("row")).Count(); n != 2 {
		t.Fatalf("want 2 rows, got %d", n)
	}

	// Search narrows to the match.
	mustFill(t, page, "search-input", "SALE")
	mustClick(t, page, "search-btn")
	expectCount(t, page, "row", 1)
	if txt := text(t, rowLocator(page, "a", "row-category")); txt != "Promotional" {
		t.Errorf("unexpected surviving row category %q", txt)
	}

	// No match shows the empty state.
	mustFill(t, page, "search-input", "zzz-nope-zzz")
	mustClick(t, page, "search-btn")
	if err := page.Locator(testid("empty")).WaitFor(); err != nil {
		t.Errorf("expected empty state: %v", err)
	}

	// Clear restores the full list.
	mustClick(t, page, "search-clear")
	expectCount(t, page, "row", 2)
}

func TestReviewStats(t *testing.T) {
	h := newHarness(t)
	seedDecision(t, h, store.Decision{EmailID: "a", Category: "Promotional", Action: "dry_run", UsedLLM: true})
	seedDecision(t, h, store.Decision{EmailID: "b", Category: "Promotional", Action: "dry_run", UsedLLM: true, LowConfidence: true})
	seedDecision(t, h, store.Decision{EmailID: "c", Category: "Promotional", Action: "moved"})

	page := newPage(t)
	login(t, page, h.ts.URL)

	if got := text(t, page.Locator(testid("stat-total"))); got != "3" {
		t.Errorf("total = %q, want 3", got)
	}
	if got := text(t, page.Locator(testid("stat-low"))); got != "1" {
		t.Errorf("low = %q, want 1", got)
	}
	if got := text(t, page.Locator(testid("stat-claude"))); got != "2" {
		t.Errorf("claude = %q, want 2", got)
	}
}

func TestReviewSortToggle(t *testing.T) {
	h := newHarness(t)
	seedDecision(t, h, store.Decision{EmailID: "lo", Category: "Promotional", Action: "dry_run", Confidence: 0.2})
	seedDecision(t, h, store.Decision{EmailID: "hi", Category: "Promotional", Action: "dry_run", Confidence: 0.9})

	page := newPage(t)
	login(t, page, h.ts.URL)

	// Default confidence sort is descending (highest first).
	mustClick(t, page, "sort-confidence")
	waitTestidText(t, page, "sort-confidence", "▼")
	if first := firstRowEmail(t, page); first != "hi" {
		t.Errorf("desc sort should put hi first, got %q", first)
	}
	// Clicking again flips to ascending.
	mustClick(t, page, "sort-confidence")
	waitTestidText(t, page, "sort-confidence", "▲")
	if first := firstRowEmail(t, page); first != "lo" {
		t.Errorf("asc sort should put lo first, got %q", first)
	}
}

func TestReviewPagination(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 60; i++ {
		seedDecision(t, h, store.Decision{
			EmailID: "e" + strconv.Itoa(i), Category: "Promotional", Action: "moved",
		})
	}
	page := newPage(t)
	login(t, page, h.ts.URL)

	expectCount(t, page, "row", 50)
	if n, _ := page.Locator(testid("page-newer")).Count(); n != 0 {
		t.Error("page 1 should not offer Newer")
	}
	mustClick(t, page, "page-older")
	expectCount(t, page, "row", 10)
	if n, _ := page.Locator(testid("page-newer")).Count(); n != 1 {
		t.Error("page 2 should offer Newer")
	}
}

func TestReviewTeachSoft(t *testing.T) {
	h := newHarness(t)
	seedDecision(t, h, store.Decision{EmailID: "a", Sender: "deals@shop.example", Subject: "Deal", Category: "Promotional", Action: "dry_run", Confidence: 0.9})

	page := newPage(t)
	login(t, page, h.ts.URL)

	selectRow(t, page, "a", "Important")
	if err := rowLocator(page, "a", "row-teach").Click(); err != nil {
		t.Fatalf("click Teach: %v", err)
	}
	// The submit is an async htmx swap; poll the store for the effect.
	if !eventually(t, 5*time.Second, func() bool {
		n, _ := h.store.DomainCategoryCount("shop.example", "Important")
		return n == 1
	}) {
		t.Error("Teach should record one observation")
	}
	if _, _, ok := h.store.SenderOverride("deals@shop.example", "shop.example"); ok {
		t.Error("Teach must not create a blanket sender rule")
	}
}

func TestReviewMoveAndTeachMovesMail(t *testing.T) {
	h := newHarness(t)
	h.jmap.addInboxEmail("a", "deals@shop.example", "Weekly deals")
	seedDecision(t, h, store.Decision{EmailID: "a", Sender: "deals@shop.example", Subject: "Weekly deals", Category: "Promotional", Action: "dry_run", Confidence: 0.9})

	page := newPage(t)
	login(t, page, h.ts.URL)

	selectRow(t, page, "a", "Promotional")
	if err := rowLocator(page, "a", "row-move-teach").Click(); err != nil {
		t.Fatalf("click Move & teach: %v", err)
	}
	// htmx submits async; poll until the refile has landed.
	if !eventually(t, 5*time.Second, func() bool {
		return h.jmap.mailboxOf("a")["mb-Promotions"]
	}) {
		t.Errorf("email should be in Promotions, got %v", h.jmap.mailboxOf("a"))
	}
	if n, _ := h.store.DomainCategoryCount("shop.example", "Promotional"); n != 1 {
		t.Errorf("Move & teach should also record an observation, got %d", n)
	}
}

func TestReviewSweepApplyMovesDespiteDryRun(t *testing.T) {
	h := newHarness(t) // dry-run is ON by default
	h.jmap.addInboxEmail("a", "deals@shop.example", "Deal A")
	h.jmap.addInboxEmail("b", "promo@store.example", "Deal B")

	page := newPage(t)
	login(t, page, h.ts.URL)

	mustClick(t, page, "btn-sweep-apply") // async
	moved := eventually(t, 10*time.Second, func() bool {
		return h.jmap.mailboxOf("a")["mb-Promotions"] && h.jmap.mailboxOf("b")["mb-Promotions"]
	})
	if !moved {
		t.Error("sweep apply should move inbox mail even while dry-run is on")
	}
}

func TestReviewSweepPreviewDoesNotMove(t *testing.T) {
	h := newHarness(t)
	h.jmap.addInboxEmail("a", "deals@shop.example", "Deal A")

	page := newPage(t)
	login(t, page, h.ts.URL)

	mustClick(t, page, "btn-sweep-preview") // async
	// Wait until the preview decision is recorded...
	logged := eventually(t, 10*time.Second, func() bool {
		pend, _ := h.store.PendingPreviewDecisions()
		return len(pend) == 1
	})
	if !logged {
		t.Fatal("preview should record a dry_run decision")
	}
	// ...but the mail must NOT have moved.
	if mb := h.jmap.mailboxOf("a"); mb["mb-Promotions"] {
		t.Error("preview must not move mail")
	}
}

func TestReviewApplyReviewed(t *testing.T) {
	h := newHarness(t)
	h.jmap.addInboxEmail("a", "deals@shop.example", "Deal A")
	seedDecision(t, h, store.Decision{EmailID: "a", Sender: "deals@shop.example", Subject: "Deal A", Category: "Promotional", Action: "dry_run", Confidence: 0.9})

	page := newPage(t)
	login(t, page, h.ts.URL)

	mustClick(t, page, "btn-apply-reviewed") // async
	applied := eventually(t, 10*time.Second, func() bool {
		pend, _ := h.store.PendingPreviewDecisions()
		return h.jmap.mailboxOf("a")["mb-Promotions"] && len(pend) == 0
	})
	if !applied {
		t.Error("apply-reviewed should file the previewed mail and clear the preview row")
	}
}

func TestReviewRunTriage(t *testing.T) {
	h := newHarness(t)
	page := newPage(t)
	login(t, page, h.ts.URL)

	mustClick(t, page, "btn-run")
	if err := page.GetByText("Triage started.").WaitFor(); err != nil {
		t.Errorf("expected the triage-started flash: %v", err)
	}
}

// --- small helpers ---

func mustFill(t *testing.T, page playwright.Page, tid, val string) {
	t.Helper()
	if err := page.Locator(testid(tid)).Fill(val); err != nil {
		t.Fatalf("fill %s: %v", tid, err)
	}
}

func mustClick(t *testing.T, page playwright.Page, tid string) {
	t.Helper()
	if err := page.Locator(testid(tid)).Click(); err != nil {
		t.Fatalf("click %s: %v", tid, err)
	}
}

func mustWaitDashboard(t *testing.T, page playwright.Page) {
	t.Helper()
	if err := page.Locator(testid("stat-total")).WaitFor(); err != nil {
		t.Fatalf("dashboard did not reload: %v", err)
	}
}

func selectRow(t *testing.T, page playwright.Page, emailID, category string) {
	t.Helper()
	if _, err := rowLocator(page, emailID, "row-select").SelectOption(
		playwright.SelectOptionValues{Values: &[]string{category}}); err != nil {
		t.Fatalf("select category in row %s: %v", emailID, err)
	}
}

func text(t *testing.T, loc playwright.Locator) string {
	t.Helper()
	s, err := loc.TextContent()
	if err != nil {
		t.Fatalf("text content: %v", err)
	}
	return strings.TrimSpace(s)
}

// expectCount waits for a testid to resolve to exactly want elements. With
// hx-boost, navigations are async swaps, so a one-shot Count can race the swap.
func expectCount(t *testing.T, page playwright.Page, tid string, want int) {
	t.Helper()
	ok := eventually(t, 5*time.Second, func() bool {
		n, _ := page.Locator(testid(tid)).Count()
		return n == want
	})
	if !ok {
		n, _ := page.Locator(testid(tid)).Count()
		t.Fatalf("want %d %q elements, got %d", want, tid, n)
	}
}

// waitTestidText waits until a testid's text contains substr.
func waitTestidText(t *testing.T, page playwright.Page, tid, substr string) {
	t.Helper()
	ok := eventually(t, 5*time.Second, func() bool {
		return strings.Contains(text(t, page.Locator(testid(tid))), substr)
	})
	if !ok {
		t.Fatalf("%q never contained %q (got %q)", tid, substr, text(t, page.Locator(testid(tid))))
	}
}

func firstRowEmail(t *testing.T, page playwright.Page) string {
	t.Helper()
	v, err := page.Locator(testid("row")).First().GetAttribute("data-email")
	if err != nil {
		t.Fatalf("first row email: %v", err)
	}
	return v
}
