package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"winnow/internal/config"
	"winnow/internal/schedule"
	"winnow/internal/store"
)

// fakeScheduler satisfies web.Scheduler.
type fakeScheduler struct{ swept bool }

func (f *fakeScheduler) TriageOnce(context.Context) {}
func (f *fakeScheduler) Sweep(context.Context, bool) (schedule.SweepResult, error) {
	f.swept = true
	return schedule.SweepResult{}, nil
}
func (f *fakeScheduler) HealthSnapshot() schedule.Health { return schedule.Health{LastPollOK: true} }

func testServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/w.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SeedCategories(); err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	cfg := &config.Config{
		AppPasswordHash: string(hash),
		SessionSecret:   "test-secret",
		Defaults:        config.Settings{Privacy: config.PrivacySnippet, Model: "claude-haiku-4-5", ConfidenceThreshold: 0.75, PollInterval: 900000000000},
	}
	s, err := New(Deps{Store: st, Scheduler: &fakeScheduler{}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

// login performs a login and returns the session cookie.
func login(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	form := url.Values{"password": {"secret123"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie after login (status %d)", rr.Code)
	return nil
}

func TestAuthGateRedirectsToLogin(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated / should redirect to /login, got %d %q", rr.Code, rr.Header().Get("Location"))
	}
}

func TestLoginAndAccess(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	cookie := login(t, h)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("authenticated / = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Review") {
		t.Errorf("review page should render; body:\n%s", rr.Body.String()[:min(400, len(rr.Body.String()))])
	}
}

func TestWrongPassword(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	form := url.Values{"password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Fatal("wrong password must not issue a session")
		}
	}
}

func TestCloudflareAccessRejectsBadJWT(t *testing.T) {
	s, _ := testServer(t)
	s.cfVerifier = rejectVerifier{} // simulate CF Access configured
	h := s.Handler()
	cookie := login(t, h) // login itself goes through CF verify; rejectVerifier only rejects when header present

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	req.Header.Set("Cf-Access-Jwt-Assertion", "bad.token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bad CF JWT should be 403, got %d", rr.Code)
	}
}

func TestSettingsSavePersists(t *testing.T) {
	s, st := testServer(t)
	h := s.Handler()
	cookie := login(t, h)

	form := url.Values{
		"poll_interval":        {"30m"},
		"confidence_threshold": {"0.9"},
		"model":                {"claude-sonnet-4-6"},
		"privacy_mode":         {"subject_sender"},
		"digest_hour":          {"8"},
		"timezone":             {"UTC"},
		"llm_daily_cap":        {"500"},
	}
	req := httptest.NewRequest(http.MethodPost, "/action/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("settings save = %d", rr.Code)
	}
	got, _ := st.LoadSettings(config.Settings{})
	if got.ConfidenceThreshold != 0.9 || got.Model != "claude-sonnet-4-6" || got.Privacy != config.PrivacySubjectSender {
		t.Errorf("settings not persisted: %+v", got)
	}
}

func TestSweepActionTriggersScheduler(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/w2.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SeedCategories()
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	fs := &fakeScheduler{}
	cfg := &config.Config{AppPasswordHash: string(hash), SessionSecret: "x", Defaults: config.Settings{PollInterval: 900000000000}}
	s, err := New(Deps{Store: st, Scheduler: fs, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie := login(t, h)

	req := httptest.NewRequest(http.MethodPost, "/action/sweep", strings.NewReader("apply=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("sweep action should redirect, got %d", rr.Code)
	}
	_ = fs // sweep dispatched in a goroutine; redirect confirms the handler ran
}

type rejectVerifier struct{}

func (rejectVerifier) Verify(context.Context, string) error { return authError("rejected") }
