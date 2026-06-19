package jmap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeServer serves the JMAP session resource and an /api endpoint whose
// behavior is driven by a per-method handler map.
type fakeServer struct {
	ts       *httptest.Server
	handlers map[string]func(args json.RawMessage) (string, any) // method -> (responseName, responseArgs)
	lastReq  apiRequest
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{handlers: map[string]func(json.RawMessage) (string, any){}}
	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"apiUrl":   f.ts.URL + "/api",
			"username": "me@example.com",
			"primaryAccounts": map[string]string{
				CapMail:       "acc1",
				CapSubmission: "acc1",
			},
			"capabilities": map[string]any{
				CapCore: map[string]any{}, CapMail: map[string]any{},
				CapSubmission: map[string]any{}, CapSieve: map[string]any{},
			},
		})
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		var req apiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.lastReq = req
		var responses []invocation
		for _, call := range req.MethodCalls {
			h, ok := f.handlers[call.Name]
			if !ok {
				responses = append(responses, mkInvocation(t, "error", map[string]any{"type": "unknownMethod"}, call.ID))
				continue
			}
			name, args := h(call.Args)
			responses = append(responses, mkInvocation(t, name, args, call.ID))
		}
		json.NewEncoder(w).Encode(apiResponse{MethodResponses: responses, SessionState: "s1"})
	})
	f.ts = httptest.NewServer(mux)
	t.Cleanup(f.ts.Close)
	return f
}

func mkInvocation(t *testing.T, name string, args any, id string) invocation {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal response args: %v", err)
	}
	return invocation{Name: name, Args: raw, ID: id}
}

func (f *fakeServer) client() *Client {
	return New("test-token", WithSessionURL(f.ts.URL+"/session"))
}

func TestSession(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	s, err := c.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.AccountID() != "acc1" {
		t.Errorf("AccountID = %q", s.AccountID())
	}
	if s.Username != "me@example.com" {
		t.Errorf("Username = %q", s.Username)
	}
	if !s.HasCapability(CapSieve) {
		t.Error("expected sieve capability")
	}
}

func TestMailboxesAndEnsure(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Mailbox/get"] = func(json.RawMessage) (string, any) {
		return "Mailbox/get", map[string]any{
			"accountId": "acc1", "state": "1",
			"list": []Mailbox{
				{ID: "mb-inbox", Name: "Inbox", Role: "inbox"},
				{ID: "mb-promos", Name: "Promotions"},
			},
		}
	}
	c := f.client()

	inbox, ok, err := c.MailboxByRole(context.Background(), "inbox")
	if err != nil || !ok || inbox.ID != "mb-inbox" {
		t.Fatalf("inbox lookup: %v ok=%v id=%q", err, ok, inbox.ID)
	}

	// Existing folder by name → no create.
	id, err := c.EnsureMailbox(context.Background(), "promotions")
	if err != nil || id != "mb-promos" {
		t.Fatalf("EnsureMailbox existing: %v id=%q", err, id)
	}

	// Missing folder → create.
	f.handlers["Mailbox/set"] = func(json.RawMessage) (string, any) {
		return "Mailbox/set", map[string]any{
			"accountId": "acc1",
			"created":   map[string]any{"new": map[string]any{"id": "mb-social"}},
		}
	}
	id, err = c.EnsureMailbox(context.Background(), "Social")
	if err != nil || id != "mb-social" {
		t.Fatalf("EnsureMailbox create: %v id=%q", err, id)
	}
}

func TestEmailChangesAndFallback(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()

	// Happy path.
	f.handlers["Email/changes"] = func(json.RawMessage) (string, any) {
		return "Email/changes", map[string]any{
			"accountId": "acc1", "oldState": "1", "newState": "2",
			"hasMoreChanges": false,
			"created":        []string{"e1", "e2"},
			"updated":        []string{},
			"destroyed":      []string{},
		}
	}
	ch, err := c.EmailChanges(context.Background(), "1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if ch.NewState != "2" || len(ch.Created) != 2 {
		t.Errorf("changes = %+v", ch)
	}

	// cannotCalculateChanges error must be detectable.
	f.handlers["Email/changes"] = func(json.RawMessage) (string, any) {
		return "error", map[string]any{"type": "cannotCalculateChanges"}
	}
	_, err = c.EmailChanges(context.Background(), "stale", 100)
	if !IsCannotCalculateChanges(err) {
		t.Fatalf("expected cannotCalculateChanges, got %v", err)
	}
}

func TestGetEmailsParsesHeaders(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Email/get"] = func(json.RawMessage) (string, any) {
		return "Email/get", map[string]any{
			"accountId": "acc1", "state": "1",
			"list": []map[string]any{{
				"id":      "e1",
				"from":    []map[string]any{{"name": "Deals", "email": "Deals@Retailer.com"}},
				"subject": "40% off",
				"preview": "limited time",
				"header:List-Unsubscribe:asText":      "<mailto:unsub@retailer.com>",
				"header:List-Unsubscribe-Post:asText": "List-Unsubscribe=One-Click",
			}},
		}
	}
	c := f.client()
	emails, err := c.GetEmails(context.Background(), []string{"e1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 {
		t.Fatalf("got %d emails", len(emails))
	}
	e := emails[0]
	if e.SenderEmail() != "deals@retailer.com" {
		t.Errorf("SenderEmail = %q (want lowercased)", e.SenderEmail())
	}
	if e.ListUnsubscribe == "" || e.ListUnsubscribePost == "" {
		t.Errorf("unsubscribe headers not parsed: %+v", e)
	}
}

func TestUpdateEmailsPayload(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Email/set"] = func(args json.RawMessage) (string, any) {
		// Assert the patch shape: mailboxIds replaced, keyword pointer set.
		var got struct {
			Update map[string]map[string]json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(args, &got); err != nil {
			t.Fatalf("decode set args: %v", err)
		}
		patch, ok := got.Update["e1"]
		if !ok {
			t.Fatalf("no patch for e1: %s", args)
		}
		if _, ok := patch["mailboxIds"]; !ok {
			t.Errorf("expected mailboxIds in patch: %s", args)
		}
		if _, ok := patch["keywords/$seen"]; !ok {
			t.Errorf("expected keywords/$seen pointer patch: %s", args)
		}
		return "Email/set", map[string]any{"accountId": "acc1"}
	}
	c := f.client()
	notUpdated, err := c.UpdateEmails(context.Background(), []EmailUpdate{{
		ID:          "e1",
		MailboxIDs:  map[string]bool{"mb-promos": true},
		SetKeywords: map[string]bool{"$seen": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(notUpdated) != 0 {
		t.Errorf("notUpdated = %v", notUpdated)
	}
}

func TestSieveValidateAndPut(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()

	f.handlers["SieveScript/validate"] = func(json.RawMessage) (string, any) {
		return "SieveScript/validate", map[string]any{"error": nil}
	}
	if err := c.ValidateSieve(context.Background(), "keep;"); err != nil {
		t.Fatalf("ValidateSieve: %v", err)
	}

	f.handlers["SieveScript/validate"] = func(json.RawMessage) (string, any) {
		return "SieveScript/validate", map[string]any{
			"error": map[string]any{"type": "invalidSieve", "description": "boom"},
		}
	}
	if err := c.ValidateSieve(context.Background(), "garbage"); err == nil {
		t.Fatal("expected invalid sieve error")
	}

	f.handlers["SieveScript/set"] = func(json.RawMessage) (string, any) {
		return "SieveScript/set", map[string]any{
			"created": map[string]any{"new": map[string]any{"id": "scr1"}},
		}
	}
	id, err := c.PutActiveSieve(context.Background(), "winnow", "keep;", "")
	if err != nil || id != "scr1" {
		t.Fatalf("PutActiveSieve create: %v id=%q", err, id)
	}
}

func TestPrimaryIdentity(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Identity/get"] = func(json.RawMessage) (string, any) {
		return "Identity/get", map[string]any{
			"accountId": "acc1",
			"list": []Identity{
				{ID: "id-other", Email: "alias@example.com"},
				{ID: "id-me", Email: "me@example.com"},
			},
		}
	}
	c := f.client()
	id, err := c.PrimaryIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "me@example.com" {
		t.Errorf("PrimaryIdentity = %q, want session username match", id.Email)
	}
}
