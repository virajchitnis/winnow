package unsubscribe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"winnow/internal/jmap"
	"winnow/internal/store"
)

type fakeMailer struct {
	sent *jmap.OutgoingMessage
}

func (f *fakeMailer) PrimaryIdentity(context.Context) (jmap.Identity, error) {
	return jmap.Identity{ID: "id1", Email: "me@example.com"}, nil
}
func (f *fakeMailer) SendEmail(_ context.Context, msg jmap.OutgoingMessage) error {
	f.sent = &msg
	return nil
}

func TestOneClickRefusesNonHTTPS(t *testing.T) {
	fetched := false
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		fetched = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(nil)}, nil
	})
	e := NewExecutor(&fakeMailer{}, WithDoer(doer))
	if err := e.Execute(context.Background(), store.UnsubMethodOneClick, "http://insecure.example/u"); err == nil {
		t.Fatal("expected non-https one-click to be refused")
	}
	if fetched {
		t.Fatal("must not POST to a non-https target")
	}
}

func TestOneClickHTTPSMechanics(t *testing.T) {
	var gotBody, gotCT, gotMethod string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()

	e := NewExecutor(&fakeMailer{}, WithDoer(srv.Client())) // TLS client trusts the test server
	if err := e.Execute(context.Background(), store.UnsubMethodOneClick, srv.URL); err != nil {
		t.Fatalf("one-click: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody != "List-Unsubscribe=One-Click" {
		t.Errorf("body = %q", gotBody)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q", gotCT)
	}
}

func TestMailtoSends(t *testing.T) {
	fm := &fakeMailer{}
	e := NewExecutor(fm)
	err := e.Execute(context.Background(), store.UnsubMethodMailto, "unsub@x.example?subject=Unsubscribe%20me")
	if err != nil {
		t.Fatal(err)
	}
	if fm.sent == nil {
		t.Fatal("no email sent")
	}
	if fm.sent.To[0] != "unsub@x.example" || fm.sent.Subject != "Unsubscribe me" {
		t.Errorf("sent = %+v", fm.sent)
	}
}

func TestBareHTTPSNeverFetched(t *testing.T) {
	fetched := false
	// A Doer that records if it is ever called.
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		fetched = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(nil)}, nil
	})
	e := NewExecutor(&fakeMailer{}, WithDoer(doer))
	err := e.Execute(context.Background(), store.UnsubMethodHTTP, "https://tracking.example/click?id=1")
	if !errors.Is(err, ErrManual) {
		t.Fatalf("expected ErrManual, got %v", err)
	}
	if fetched {
		t.Fatal("bare HTTPS link must NEVER be auto-fetched")
	}
}

func TestParseMailto(t *testing.T) {
	addr, subj, body := parseMailto("a@b.example?subject=hi&body=stop")
	if addr != "a@b.example" || subj != "hi" || body != "stop" {
		t.Errorf("parseMailto = %q,%q,%q", addr, subj, body)
	}
	addr, subj, _ = parseMailto("plain@b.example")
	if addr != "plain@b.example" || subj != "" {
		t.Errorf("parseMailto plain = %q,%q", addr, subj)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
