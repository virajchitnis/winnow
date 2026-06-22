//go:build e2e

// Package e2e is a browser regression suite. It drives the real dashboard in
// headless Chromium against an in-process backend: a real SQLite store and a
// real schedule.Scheduler wired to an in-memory fake JMAP + fake classifier, so
// triage / sweep / refile / apply-reviewed run for real through the UI (mail
// movements are tracked in the fake), without touching Fastmail or Anthropic.
package e2e

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"golang.org/x/crypto/bcrypt"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/config"
	"winnow/internal/jmap"
	"winnow/internal/schedule"
	"winnow/internal/store"
	"winnow/internal/web"
)

const testPassword = "e2e-test-pass-123"

// Shared browser for the whole package (cheap per-test contexts isolate state).
var (
	pw      *playwright.Playwright
	browser playwright.Browser
)

func TestMain(m *testing.M) {
	var err error
	pw, err = playwright.Run()
	if err != nil {
		panic("playwright run (install the browser: `playwright install --with-deps chromium`): " + err.Error())
	}
	browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Args: []string{"--no-sandbox"}, // required when running as root (containers)
	})
	if err != nil {
		panic("launch chromium: " + err.Error())
	}
	code := m.Run()
	_ = browser.Close()
	_ = pw.Stop()
	os.Exit(code)
}

// fakeJMAP is an in-memory stand-in for the Fastmail JMAP client, satisfying
// both schedule.Mailer and actions.JMAP. Mail moves/flags are applied to its
// in-memory state and recorded so tests can assert them.
type fakeJMAP struct {
	mu       sync.Mutex
	inbox    jmap.Mailbox
	state    string
	changes  *jmap.Changes
	emails   map[string]jmap.Email
	updates  []jmap.EmailUpdate
	folderID map[string]string
}

func newFakeJMAP() *fakeJMAP {
	return &fakeJMAP{
		inbox:    jmap.Mailbox{ID: "mb-inbox", Role: "inbox", Name: "Inbox"},
		state:    "s1",
		emails:   map[string]jmap.Email{},
		folderID: map[string]string{},
	}
}

func (f *fakeJMAP) MailboxByRole(context.Context, string) (jmap.Mailbox, bool, error) {
	return f.inbox, true, nil
}
func (f *fakeJMAP) MailboxState(context.Context) (string, error) { return f.state, nil }
func (f *fakeJMAP) EmailChanges(context.Context, string, int) (*jmap.Changes, error) {
	if f.changes != nil {
		return f.changes, nil
	}
	return &jmap.Changes{NewState: f.state}, nil
}
func (f *fakeJMAP) QueryInbox(context.Context, string, int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, e := range f.emails {
		if e.MailboxIDs[f.inbox.ID] {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (f *fakeJMAP) GetEmails(_ context.Context, ids []string) ([]jmap.Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []jmap.Email
	for _, id := range ids {
		if e, ok := f.emails[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeJMAP) EnsureMailbox(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "mb-" + name
	f.folderID[name] = id
	return id, nil
}
func (f *fakeJMAP) UpdateEmails(_ context.Context, ups []jmap.EmailUpdate) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range ups {
		f.updates = append(f.updates, u)
		e := f.emails[u.ID]
		if u.MailboxIDs != nil {
			e.MailboxIDs = u.MailboxIDs
		}
		if u.SetKeywords != nil {
			if e.Keywords == nil {
				e.Keywords = map[string]bool{}
			}
			for k, v := range u.SetKeywords {
				e.Keywords[k] = v
			}
		}
		f.emails[u.ID] = e
	}
	return nil, nil
}

// mailboxOf returns the mailbox ids currently set on an email (test helper).
func (f *fakeJMAP) mailboxOf(id string) map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.emails[id].MailboxIDs
}

// addInboxEmail seeds an email sitting in the inbox.
func (f *fakeJMAP) addInboxEmail(id, sender, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emails[id] = jmap.Email{
		ID:         id,
		MailboxIDs: map[string]bool{f.inbox.ID: true},
		From:       []jmap.EmailAddress{{Email: sender}},
		Subject:    subject,
	}
}

// fakeClassifier returns a deterministic category so triage/sweep are testable.
type fakeClassifier struct{ category string }

func (c fakeClassifier) Classify(_ context.Context, req classify.Request) ([]classify.Result, error) {
	out := make([]classify.Result, len(req.Mails))
	for i := range req.Mails {
		out[i] = classify.Result{
			Category: c.category, Confidence: 0.95, Source: classify.SourceLLM, UsedLLM: true,
			Summary: "test", Reason: "test",
		}
	}
	return out, nil
}

// harness bundles the running server and its backing fakes for assertions.
type harness struct {
	ts    *httptest.Server
	store *store.Store
	jmap  *fakeJMAP
}

// newHarness wires a real store + real scheduler (fake JMAP/classifier) behind
// the dashboard and starts an httptest server.
func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/e2e.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	defaults := config.Settings{
		DryRun: true, Timezone: "UTC", PollInterval: 15 * time.Minute,
		ConfidenceThreshold: 0.75, LLMDailyCap: 10000,
		Model: "claude-haiku-4-5", Privacy: config.PrivacySnippet,
	}
	if err := st.SeedSettings(defaults); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := st.SeedCategories(); err != nil {
		t.Fatalf("seed categories: %v", err)
	}

	fj := newFakeJMAP()
	sched := schedule.New(schedule.Deps{
		Store: st, Mail: fj, Classifier: fakeClassifier{category: "Promotional"},
		Applier: actions.NewApplier(fj), Defaults: defaults,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})

	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	cfg := &config.Config{
		AppPasswordHash: string(hash), SessionSecret: "e2e-session-secret", Defaults: defaults,
	}
	srv, err := web.New(web.Deps{Store: st, Scheduler: sched, Config: cfg})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{ts: ts, store: st, jmap: fj}
}

// newPage opens an isolated browser context + page (fresh cookies).
func newPage(t *testing.T) playwright.Page {
	t.Helper()
	ctx, err := browser.NewContext()
	if err != nil {
		t.Fatalf("new context: %v", err)
	}
	t.Cleanup(func() { _ = ctx.Close() })
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	return page
}

// testid returns a data-testid CSS selector.
func testid(id string) string { return "[data-testid=\"" + id + "\"]" }

// login signs in and waits for the Review page.
func login(t *testing.T, page playwright.Page, base string) {
	t.Helper()
	if _, err := page.Goto(base + "/"); err != nil {
		t.Fatalf("goto: %v", err)
	}
	if err := page.Locator(testid("login-password")).Fill(testPassword); err != nil {
		t.Fatalf("fill password: %v", err)
	}
	if err := page.Locator(testid("login-submit")).Click(); err != nil {
		t.Fatalf("click sign in: %v", err)
	}
	if err := page.Locator(testid("stat-total")).WaitFor(); err != nil {
		t.Fatalf("expected Review after login: %v", err)
	}
}

// eventually polls fn until it returns true or the timeout elapses (for the
// dashboard actions that run asynchronously in a goroutine).
func eventually(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
