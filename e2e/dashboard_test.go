//go:build e2e

// Package e2e drives the real dashboard UI in a headless browser against a
// fully in-process backend (real SQLite, a fake scheduler — no Fastmail or
// Anthropic calls). It is build-tagged so it only runs under `-tags e2e` with a
// Playwright browser installed; CI runs it in a dedicated job.
package e2e

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"golang.org/x/crypto/bcrypt"

	"winnow/internal/config"
	"winnow/internal/schedule"
	"winnow/internal/store"
	"winnow/internal/web"
)

const testPassword = "e2e-test-pass-123"

// fakeScheduler satisfies web.Scheduler without any external calls.
type fakeScheduler struct{}

func (fakeScheduler) TriageOnce(context.Context) {}
func (fakeScheduler) Sweep(context.Context, bool) (schedule.SweepResult, error) {
	return schedule.SweepResult{}, nil
}
func (fakeScheduler) Refile(context.Context, string, string) (string, error) { return "moved", nil }
func (fakeScheduler) ApplyReviewed(context.Context) (int, error)             { return 0, nil }
func (fakeScheduler) HealthSnapshot() schedule.Health {
	return schedule.Health{LastPollOK: true}
}

// newServer wires a real store (seeded) behind the dashboard handler.
func newServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/e2e.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	defaults := config.Settings{
		PollInterval: 15 * time.Minute, ConfidenceThreshold: 0.75,
		Model: "claude-haiku-4-5", Privacy: config.PrivacySnippet, Timezone: "UTC",
	}
	if err := st.SeedSettings(defaults); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := st.SeedCategories(); err != nil {
		t.Fatalf("seed categories: %v", err)
	}
	// One previewed decision so the Review tab has a row to act on.
	if err := st.RecordDecision(store.Decision{
		EmailID: "e1", Sender: "deals@shop.example", Subject: "Weekly deals: 40% off",
		Category: "Promotional", Action: "dry_run", Confidence: 0.9, UsedLLM: true,
	}); err != nil {
		t.Fatalf("seed decision: %v", err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	cfg := &config.Config{
		AppPasswordHash: string(hash), SessionSecret: "e2e-session-secret",
		Defaults: defaults,
	}
	srv, err := web.New(web.Deps{Store: st, Scheduler: fakeScheduler{}, Config: cfg})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func TestDashboardE2E(t *testing.T) {
	ts, st := newServer(t)

	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("playwright run (is the browser installed? `playwright install chromium`): %v", err)
	}
	defer pw.Stop()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		// --no-sandbox is required when running as root (e.g. in a container).
		Args: []string{"--no-sandbox"},
	})
	if err != nil {
		t.Fatalf("launch chromium: %v", err)
	}
	defer browser.Close()
	page, err := browser.NewPage()
	if err != nil {
		t.Fatalf("new page: %v", err)
	}

	// --- Auth gate: an unauthenticated visit lands on the sign-in form. ---
	if _, err := page.Goto(ts.URL + "/"); err != nil {
		t.Fatalf("goto: %v", err)
	}
	if err := page.Locator("input[name=password]").WaitFor(); err != nil {
		t.Fatalf("expected the login form: %v", err)
	}

	// --- Log in. ---
	if err := page.Locator("input[name=password]").Fill(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := page.GetByRole("button", playwright.PageGetByRoleOptions{
		Name: "Sign in",
	}).Click(); err != nil {
		t.Fatal(err)
	}
	// Review heading confirms we're authenticated and on the home tab.
	if err := page.GetByRole("heading", playwright.PageGetByRoleOptions{
		Name: "Review",
	}).WaitFor(); err != nil {
		t.Fatalf("expected Review after login: %v", err)
	}
	if html := content(t, page); !strings.Contains(html, "Weekly deals") {
		t.Errorf("seeded decision not shown on Review")
	}

	// --- Search filters the decisions list. ---
	if err := page.Locator("input[name=q]").Fill("Weekly"); err != nil {
		t.Fatal(err)
	}
	if err := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: "Search"}).Click(); err != nil {
		t.Fatal(err)
	}
	if html := content(t, page); !strings.Contains(html, "Weekly deals") {
		t.Errorf("matching row should remain after search")
	}
	if err := page.Locator("input[name=q]").Fill("zzz-no-match-zzz"); err != nil {
		t.Fatal(err)
	}
	if err := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: "Search"}).Click(); err != nil {
		t.Fatal(err)
	}
	if html := content(t, page); !strings.Contains(html, "No decisions match") {
		t.Errorf("non-matching search should show the empty-state message")
	}

	// --- Teach a correction (soft): records an observation, not a hard rule. ---
	if _, err := page.Goto(ts.URL + "/"); err != nil { // clear the search
		t.Fatal(err)
	}
	if _, err := page.Locator("select[name=category]").First().SelectOption(
		playwright.SelectOptionValues{Values: &[]string{"Important"}}); err != nil {
		t.Fatalf("select category: %v", err)
	}
	if err := page.GetByRole("button", playwright.PageGetByRoleOptions{
		Name: "Teach", Exact: playwright.Bool(true),
	}).First().Click(); err != nil {
		t.Fatalf("click Teach: %v", err)
	}
	if err := page.GetByRole("heading", playwright.PageGetByRoleOptions{Name: "Review"}).WaitFor(); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.DomainCategoryCount("shop.example", "Important"); n != 1 {
		t.Errorf("Teach should record one observation, got %d", n)
	}
	if _, _, ok := st.SenderOverride("deals@shop.example", "shop.example"); ok {
		t.Error("Teach must not create a blanket sender rule")
	}

	// --- Create a category via the Categories tab. ---
	if _, err := page.Goto(ts.URL + "/categories"); err != nil {
		t.Fatal(err)
	}
	if err := page.GetByPlaceholder("Name").Fill("E2E Bucket"); err != nil {
		t.Fatal(err)
	}
	if err := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: "Add"}).Click(); err != nil {
		t.Fatal(err)
	}
	// Category names render as editable <input value="...">, so match the value.
	if err := page.Locator(`input[value="E2E Bucket"]`).First().WaitFor(); err != nil {
		t.Fatalf("new category not shown: %v", err)
	}
	cats, _ := st.Categories()
	found := false
	for _, c := range cats {
		if c.Name == "E2E Bucket" {
			found = true
		}
	}
	if !found {
		t.Error("new category not persisted to the store")
	}
}

func content(t *testing.T, page playwright.Page) string {
	t.Helper()
	html, err := page.Content()
	if err != nil {
		t.Fatalf("page content: %v", err)
	}
	return html
}
