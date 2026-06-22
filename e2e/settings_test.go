//go:build e2e

package e2e

import (
	"errors"
	"testing"

	"winnow/internal/config"
)

func TestSettingsSave(t *testing.T) {
	h := newHarness(t) // dry-run ON by default
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/settings")

	if err := page.Locator(testid("set-confidence")).Fill("0.42"); err != nil {
		t.Fatal(err)
	}
	// Uncheck dry-run.
	if err := page.Locator(testid("set-dryrun")).Uncheck(); err != nil {
		t.Fatal(err)
	}
	mustClick(t, page, "set-save")
	// Wait for the save round-trip to complete (flash only shows after redirect).
	if err := page.GetByText("Settings saved.").WaitFor(); err != nil {
		t.Fatalf("expected settings-saved flash: %v", err)
	}

	got, _ := h.store.LoadSettings(config.Settings{})
	if got.ConfidenceThreshold != 0.42 {
		t.Errorf("threshold = %v, want 0.42", got.ConfidenceThreshold)
	}
	if got.DryRun {
		t.Error("dry-run should be off after unchecking and saving")
	}
}

func TestSettingsChangePassword(t *testing.T) {
	h := newHarness(t)
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/settings")

	const newPass = "brand-new-pass-9"
	if err := page.Locator(testid("pw-current")).Fill(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("pw-new")).Fill(newPass); err != nil {
		t.Fatal(err)
	}
	mustClick(t, page, "pw-change")
	if err := page.GetByText("Password changed.").WaitFor(); err != nil {
		t.Fatalf("expected password-changed flash: %v", err)
	}

	// Sign out, then the new password must work.
	gotoTab(t, page, h.ts.URL, "/logout")
	if err := page.Locator(testid("login-password")).Fill(newPass); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("login-submit")).Click(); err != nil {
		t.Fatal(err)
	}
	if err := page.Locator(testid("stat-total")).WaitFor(); err != nil {
		t.Fatalf("new password should log in: %v", err)
	}
}

func TestSettingsTestConnection(t *testing.T) {
	h := newHarness(t, withPingers(nil, errors.New("bad key")))
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/settings")

	mustClick(t, page, "test-fastmail")
	if err := page.GetByText("Fastmail connection OK.").WaitFor(); err != nil {
		t.Errorf("expected Fastmail OK: %v", err)
	}
	mustClick(t, page, "test-anthropic")
	if err := page.GetByText("Anthropic connection FAILED: bad key").WaitFor(); err != nil {
		t.Errorf("expected Anthropic failure flash: %v", err)
	}
}
