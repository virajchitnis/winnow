//go:build e2e

package e2e

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	"winnow/internal/store"
	"winnow/internal/unsubscribe"
)

func unsubRow(page playwright.Page, sender string) playwright.Locator {
	return page.Locator(`tr[data-sender="` + sender + `"]`)
}

func TestUnsubscribeKeep(t *testing.T) {
	h := newHarness(t)
	if err := h.store.ObserveUnsubscribe("news@shop.example", store.UnsubMethodMailto, "u@shop.example"); err != nil {
		t.Fatal(err)
	}
	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/unsubscribe")

	if err := unsubRow(page, "news@shop.example").Locator(testid("unsub-keep")).Click(); err != nil {
		t.Fatalf("click keep: %v", err)
	}
	ok := eventually(t, 5*time.Second, func() bool {
		rec, found, _ := h.store.UnsubscribeRecordBySender("news@shop.example")
		return found && rec.Status == store.UnsubKept
	})
	if !ok {
		t.Error("keep should set status to kept")
	}
}

func TestUnsubscribeStatusFilter(t *testing.T) {
	h := newHarness(t)
	_ = h.store.ObserveUnsubscribe("kept@a.example", store.UnsubMethodMailto, "u@a.example")
	_ = h.store.ObserveUnsubscribe("pending@b.example", store.UnsubMethodMailto, "u@b.example")
	_ = h.store.SetUnsubscribeStatus("kept@a.example", store.UnsubKept, false)

	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/unsubscribe")

	// All shows both; the Kept filter shows only the kept one.
	expectCount(t, page, "unsub-row", 2)
	mustClick(t, page, "filter-kept")
	expectCount(t, page, "unsub-row", 1)
	if err := unsubRow(page, "kept@a.example").WaitFor(); err != nil {
		t.Errorf("kept sender should be the one shown: %v", err)
	}
}

func TestUnsubscribeOneClickExecute(t *testing.T) {
	var hits int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	// Executor uses the TLS server's client so it trusts the self-signed cert.
	exec := unsubscribe.NewExecutor(nil, unsubscribe.WithDoer(target.Client()))
	h := newHarness(t, withUnsub(exec))
	if err := h.store.ObserveUnsubscribe("promo@store.example", store.UnsubMethodOneClick, target.URL); err != nil {
		t.Fatal(err)
	}

	page := newPage(t)
	login(t, page, h.ts.URL)
	gotoTab(t, page, h.ts.URL, "/unsubscribe")

	if err := unsubRow(page, "promo@store.example").Locator(testid("unsub-do")).Click(); err != nil {
		t.Fatalf("click unsubscribe: %v", err)
	}
	ok := eventually(t, 5*time.Second, func() bool {
		rec, found, _ := h.store.UnsubscribeRecordBySender("promo@store.example")
		return found && rec.Status == store.UnsubUnsubscribed
	})
	if !ok {
		t.Error("one-click unsubscribe should set status to unsubscribed")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("expected exactly one POST to the unsubscribe endpoint, got %d", hits)
	}
}
