package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"winnow/internal/store"
)

// getPage logs in and fetches an authenticated page, returning the response.
func getPage(t *testing.T, h http.Handler, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func post(t *testing.T, h http.Handler, cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestAllPagesRender(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	cookie := login(t, h)
	for _, p := range []string{"/", "/categories", "/senders", "/rules", "/unsubscribe", "/settings"} {
		rr := getPage(t, h, cookie, p)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s = %d", p, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "Winnow") {
			t.Errorf("GET %s did not render layout", p)
		}
	}
}

func TestHealthzPublic(t *testing.T) {
	s, _ := testServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "last_poll_ok") {
		t.Errorf("healthz = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCategoryCreateAndDelete(t *testing.T) {
	s, st := testServer(t)
	h := s.Handler()
	cookie := login(t, h)

	post(t, h, cookie, "/action/category", url.Values{
		"name": {"Receipts"}, "destination_folder": {"Receipts"},
	})
	c, ok, _ := st.CategoryByName("Receipts")
	if !ok {
		t.Fatal("category not created")
	}
	post(t, h, cookie, "/action/category/delete", url.Values{"id": {itoa(c.ID)}})
	if _, ok, _ := st.CategoryByName("Receipts"); ok {
		t.Error("category not deleted")
	}
}

func TestSenderRuleAddRemove(t *testing.T) {
	s, st := testServer(t)
	h := s.Handler()
	cookie := login(t, h)

	post(t, h, cookie, "/action/sender", url.Values{
		"pattern": {"@junk.com"}, "kind": {store.KindDenyBulk}, "category": {"Promotional"},
	})
	if rules, _ := st.SenderRules(); len(rules) != 1 {
		t.Fatalf("rule not added: %d", len(rules))
	}
	post(t, h, cookie, "/action/sender", url.Values{
		"pattern": {"@junk.com"}, "kind": {store.KindDenyBulk}, "delete": {"1"},
	})
	if rules, _ := st.SenderRules(); len(rules) != 0 {
		t.Error("rule not removed")
	}
}

func TestCorrectRecordsOverride(t *testing.T) {
	s, st := testServer(t)
	h := s.Handler()
	cookie := login(t, h)

	post(t, h, cookie, "/action/correct", url.Values{
		"email_id": {"e1"}, "sender": {"x@promo.com"}, "category": {"Promotional"},
	})
	// Promotional is a moving category -> deny-bulk rule on the domain.
	if cat, _, ok := st.SenderOverride("x@promo.com", "promo.com"); !ok || cat != "Promotional" {
		t.Errorf("correction did not create deny rule: %q,%v", cat, ok)
	}
}

func TestRuleDecisionAndUnsubKeep(t *testing.T) {
	s, st := testServer(t)
	h := s.Handler()
	cookie := login(t, h)

	_ = st.ObserveSieveCandidate("shop.com", "Promotional")
	post(t, h, cookie, "/action/rule", url.Values{
		"domain": {"shop.com"}, "category": {"Promotional"}, "status": {store.SieveApproved},
	})
	if got, _ := st.SieveCandidates(store.SieveApproved); len(got) != 1 {
		t.Error("candidate not approved")
	}

	_ = st.ObserveUnsubscribe("news@shop.com", store.UnsubMethodMailto, "u@shop.com")
	post(t, h, cookie, "/action/unsub", url.Values{
		"sender": {"news@shop.com"}, "choice": {"keep"}, "category": {"Promotional"},
	})
	rec, _, _ := st.UnsubscribeRecordBySender("news@shop.com")
	if rec.Status != store.UnsubKept {
		t.Errorf("unsub keep status = %q", rec.Status)
	}
}

func TestPasswordChange(t *testing.T) {
	s, st := testServer(t)
	h := s.Handler()
	cookie := login(t, h)

	post(t, h, cookie, "/action/password", url.Values{
		"current": {"secret123"}, "new": {"newpassword1"},
	})
	if v, ok, _ := st.GetSetting("app_password_hash"); !ok || v == "" {
		t.Fatal("password hash not stored")
	}
	// Old password should no longer log in.
	form := url.Values{"password": {"secret123"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Error("old password should no longer work")
		}
	}
}

func TestLogoutClearsSession(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	cookie := login(t, h)
	rr := getPage(t, h, cookie, "/logout")
	cleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout should clear the session cookie")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
