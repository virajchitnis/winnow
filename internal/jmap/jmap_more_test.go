package jmap

import (
	"context"
	"encoding/json"
	"testing"
)

func TestQueryInbox(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Email/query"] = func(args json.RawMessage) (string, any) {
		var got struct {
			Filter map[string]any `json:"filter"`
			Limit  int            `json:"limit"`
		}
		_ = json.Unmarshal(args, &got)
		if got.Filter["inMailbox"] != "inbox" || got.Limit != 50 {
			t.Errorf("query args = %s", args)
		}
		return "Email/query", map[string]any{"ids": []string{"e1", "e2"}}
	}
	ids, err := f.client().QueryInbox(context.Background(), "inbox", 50)
	if err != nil || len(ids) != 2 {
		t.Fatalf("QueryInbox: %v ids=%v", err, ids)
	}
}

func TestMailboxState(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Email/get"] = func(json.RawMessage) (string, any) {
		return "Email/get", map[string]any{"state": "st-9", "list": []any{}}
	}
	state, err := f.client().MailboxState(context.Background())
	if err != nil || state != "st-9" {
		t.Fatalf("MailboxState = %q err=%v", state, err)
	}
}

func TestSendEmail(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Mailbox/get"] = func(json.RawMessage) (string, any) {
		return "Mailbox/get", map[string]any{"list": []Mailbox{
			{ID: "mb-drafts", Role: "drafts"}, {ID: "mb-sent", Role: "sent"},
		}}
	}
	var draftCreated, submitted bool
	f.handlers["Email/set"] = func(args json.RawMessage) (string, any) {
		var got struct {
			Create map[string]any `json:"create"`
		}
		_ = json.Unmarshal(args, &got)
		if _, ok := got.Create["draft"]; ok {
			draftCreated = true
		}
		return "Email/set", map[string]any{}
	}
	f.handlers["EmailSubmission/set"] = func(json.RawMessage) (string, any) {
		submitted = true
		return "EmailSubmission/set", map[string]any{}
	}
	err := f.client().SendEmail(context.Background(), OutgoingMessage{
		FromIdentity: Identity{ID: "id1", Email: "me@example.com"},
		To:           []string{"me@example.com"},
		Subject:      "hi",
		Text:         "body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !draftCreated || !submitted {
		t.Errorf("draftCreated=%v submitted=%v", draftCreated, submitted)
	}
}

func TestPingRefetchesSession(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestActiveSieveScript(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["SieveScript/get"] = func(json.RawMessage) (string, any) {
		return "SieveScript/get", map[string]any{"list": []map[string]any{
			{"id": "s1", "name": "inactive", "isActive": false, "content": "x"},
			{"id": "s2", "name": "main", "isActive": true, "content": "require [\"fileinto\"];"},
		}}
	}
	script, content, ok, err := f.client().ActiveSieveScript(context.Background())
	if err != nil || !ok || script.ID != "s2" || content == "" {
		t.Fatalf("ActiveSieveScript = %+v %q ok=%v err=%v", script, content, ok, err)
	}
}
