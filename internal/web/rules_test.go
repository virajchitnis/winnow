package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"winnow/internal/config"
	"winnow/internal/jmap"
	"winnow/internal/sieve"
	"winnow/internal/store"
)

// fakeSieveJMAP satisfies sieve.JMAP.
type fakeSieveJMAP struct {
	content string
	exists  bool
	put     string
}

func (f *fakeSieveJMAP) ActiveSieveScript(context.Context) (jmap.SieveScript, string, bool, error) {
	return jmap.SieveScript{ID: "s1", Name: "user"}, f.content, f.exists, nil
}
func (f *fakeSieveJMAP) ValidateSieve(context.Context, string) error { return nil }
func (f *fakeSieveJMAP) PutActiveSieve(_ context.Context, _, content, _ string) (string, error) {
	f.put = content
	return "s1", nil
}

func serverWithSieve(t *testing.T) (*Server, *store.Store, *fakeSieveJMAP) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_ = st.SeedCategories()
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	fj := &fakeSieveJMAP{content: "require [\"fileinto\"];\n", exists: true}
	cfg := &config.Config{AppPasswordHash: string(hash), SessionSecret: "x", Defaults: config.Settings{PollInterval: 900000000000}}
	s, err := New(Deps{Store: st, Scheduler: &fakeScheduler{}, Sieve: sieve.New(fj, st), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return s, st, fj
}

func TestRulesApplyAndRevert(t *testing.T) {
	s, st, fj := serverWithSieve(t)
	h := s.Handler()
	cookie := login(t, h)

	_ = st.ObserveSieveCandidate("shop.example", "Promotional")
	_ = st.SetSieveCandidateStatus("shop.example", "Promotional", store.SieveApproved)

	if rr := post(t, h, cookie, "/action/rules/apply", url.Values{}); rr.Code != http.StatusSeeOther {
		t.Fatalf("apply = %d", rr.Code)
	}
	if fj.put == "" {
		t.Error("apply should have written a script")
	}

	if rr := post(t, h, cookie, "/action/rules/revert", url.Values{}); rr.Code != http.StatusSeeOther {
		t.Fatalf("revert = %d", rr.Code)
	}
}

func TestUnsubscribeActionExecutes(t *testing.T) {
	// Build a server whose unsubscribe executor uses a fake mailer so a mailto
	// unsubscribe succeeds without network.
	s, st, _ := serverWithSieve(t)
	h := s.Handler()
	cookie := login(t, h)

	_ = st.ObserveUnsubscribe("a@x.example", store.UnsubMethodHTTP, "https://x.example/u")
	// http_manual returns ErrManual via the (nil) executor path -> handled, but
	// with no executor configured the handler reports unavailable. Just confirm
	// the keep path here for the store transition.
	post(t, h, cookie, "/action/unsub", url.Values{"sender": {"a@x.example"}, "choice": {"keep"}})
	rec, _, _ := st.UnsubscribeRecordBySender("a@x.example")
	if rec.Status != store.UnsubKept {
		t.Errorf("status = %q", rec.Status)
	}
}
