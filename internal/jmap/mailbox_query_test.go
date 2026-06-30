package jmap

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMailboxByNameAndQuerySince(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Mailbox/get"] = func(json.RawMessage) (string, any) {
		return "Mailbox/get", map[string]any{
			"accountId": "acc1", "state": "1",
			"list": []Mailbox{
				{ID: "mb-inbox", Name: "Inbox", Role: "inbox"},
				{ID: "mb-news", Name: "Newsletters"},
			},
		}
	}
	c := f.client()

	mb, ok, err := c.MailboxByName(context.Background(), "newsletters") // case-insensitive
	if err != nil || !ok || mb.ID != "mb-news" {
		t.Fatalf("MailboxByName = (%q, %v, %v)", mb.ID, ok, err)
	}
	if _, ok, _ := c.MailboxByName(context.Background(), "Nope"); ok {
		t.Error("unknown mailbox should not be found")
	}

	f.handlers["Email/query"] = func(args json.RawMessage) (string, any) {
		return "Email/query", map[string]any{"ids": []string{"e1", "e2"}}
	}
	ids, err := c.QueryMailboxSince(context.Background(), "mb-news", time.Now().Add(-24*time.Hour), 10)
	if err != nil || len(ids) != 2 {
		t.Fatalf("QueryMailboxSince = %v, %v", ids, err)
	}
	if ids2, _ := c.QueryMailboxSince(context.Background(), "mb-news", time.Time{}, 10); len(ids2) != 2 {
		t.Errorf("zero-time query = %v", ids2)
	}
}
