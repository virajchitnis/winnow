//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	"winnow/internal/store"
)

func catRow(page playwright.Page, name string) playwright.Locator {
	return page.Locator(`tr[data-cat="` + name + `"]`)
}

func TestCategoryCreate(t *testing.T) {
	h := newHarness(t)
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/categories")

	mustFill(t, page, "cat-name", "E2E Bucket")
	mustFill(t, page, "cat-folder", "Bucket")
	mustClick(t, page, "cat-add")

	if err := catRow(page, "E2E Bucket").WaitFor(); err != nil {
		t.Fatalf("new category row not shown: %v", err)
	}
	cats, _ := h.store.Categories()
	if !hasCategory(cats, "E2E Bucket") {
		t.Error("category not persisted")
	}
}

func TestCategoryEdit(t *testing.T) {
	h := newHarness(t)
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/categories")

	// Change the built-in Promotional category's destination folder.
	folder := catRow(page, "Promotional").Locator(testid("cat-row-folder"))
	if err := folder.Fill("PromosRenamed"); err != nil {
		t.Fatal(err)
	}
	if err := catRow(page, "Promotional").Locator(testid("cat-row-save")).Click(); err != nil {
		t.Fatal(err)
	}
	// htmx submits async; poll the store for the persisted change.
	if !eventually(t, 5*time.Second, func() bool {
		cat, ok, _ := h.store.CategoryByName("Promotional")
		return ok && cat.DestinationFolder == "PromosRenamed"
	}) {
		cat, _, _ := h.store.CategoryByName("Promotional")
		t.Errorf("edit not persisted: %+v", cat)
	}
}

func TestCategoryDelete(t *testing.T) {
	h := newHarness(t)
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/categories")

	// Create a custom (deletable) category, then delete it.
	mustFill(t, page, "cat-name", "Temp Cat")
	mustClick(t, page, "cat-add")
	if err := catRow(page, "Temp Cat").WaitFor(); err != nil {
		t.Fatalf("temp category not created: %v", err)
	}
	if err := catRow(page, "Temp Cat").Locator(testid("cat-row-delete")).Click(); err != nil {
		t.Fatal(err)
	}
	// Row should disappear and the store should no longer have it.
	if err := catRow(page, "Temp Cat").WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateDetached,
	}); err != nil {
		t.Fatalf("category row should be gone: %v", err)
	}
	cats, _ := h.store.Categories()
	if hasCategory(cats, "Temp Cat") {
		t.Error("deleted category still in store")
	}
}

func hasCategory(cats []store.Category, name string) bool {
	for _, c := range cats {
		if c.Name == name {
			return true
		}
	}
	return false
}
