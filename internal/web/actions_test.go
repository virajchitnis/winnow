package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

type okPinger struct{ err error }

func (p okPinger) Ping(context.Context) error { return p.err }

func TestRunNowAction(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	cookie := login(t, h)
	rr := post(t, h, cookie, "/action/run", url.Values{})
	if rr.Code != http.StatusSeeOther {
		t.Errorf("run now = %d", rr.Code)
	}
}

func TestTestConnectionAction(t *testing.T) {
	s, _ := testServer(t)
	s.fastmailPing = okPinger{}
	s.anthropicPing = okPinger{err: errors.New("bad key")}
	h := s.Handler()
	cookie := login(t, h)

	rr := post(t, h, cookie, "/action/test", url.Values{"service": {"fastmail"}})
	if loc := rr.Header().Get("Location"); rr.Code != http.StatusSeeOther {
		t.Errorf("fastmail test = %d %s", rr.Code, loc)
	}
	rr = post(t, h, cookie, "/action/test", url.Values{"service": {"anthropic"}})
	if rr.Code != http.StatusSeeOther {
		t.Errorf("anthropic test = %d", rr.Code)
	}
	// Unknown service.
	rr = post(t, h, cookie, "/action/test", url.Values{"service": {"bogus"}})
	if rr.Code != http.StatusSeeOther {
		t.Errorf("bogus test = %d", rr.Code)
	}
}

func TestCategoryUpdatePath(t *testing.T) {
	s, st := testServer(t)
	h := s.Handler()
	cookie := login(t, h)

	promo, _, _ := st.CategoryByName("Promotional")
	post(t, h, cookie, "/action/category", url.Values{
		"id": {itoa(promo.ID)}, "name": {"Promotional"}, "destination_folder": {"Deals"},
		"mark_read": {"on"},
	})
	updated, _, _ := st.CategoryByName("Promotional")
	if updated.DestinationFolder != "Deals" || !updated.MarkRead {
		t.Errorf("category update not applied: %+v", updated)
	}
}
