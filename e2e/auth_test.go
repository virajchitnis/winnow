//go:build e2e

package e2e

import (
	"testing"

	"github.com/playwright-community/playwright-go"
)

func TestAuthGate(t *testing.T) {
	h := newHarness(t)
	page := newPage(t)

	// Unauthenticated visit shows the login form, not the dashboard.
	if _, err := page.Goto(h.ts.URL + "/"); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("login-password")).WaitFor(); err != nil {
		t.Fatalf("expected login form: %v", err)
	}
	if n, _ := page.Locator(testid("stat-total")).Count(); n != 0 {
		t.Error("dashboard content must not be visible before login")
	}

	// Wrong password is rejected and keeps you on the login form.
	if err := page.Locator(testid("login-password")).Fill("wrong-password"); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("login-submit")).Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("login-error")).WaitFor(); err != nil {
		t.Fatalf("expected an error on wrong password: %v", err)
	}

	// Correct password lets us in.
	if err := page.Locator(testid("login-password")).Fill(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("login-submit")).Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("stat-total")).WaitFor(); err != nil {
		t.Fatalf("expected dashboard after correct login: %v", err)
	}
}

// rowSelect targets a control within a specific decision row (by email id).
func rowLocator(page playwright.Page, emailID, tid string) playwright.Locator {
	return page.Locator(`tr[data-email="` + emailID + `"]`).Locator(testid(tid))
}
