package jmap

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFetchTextBodies(t *testing.T) {
	f := newFakeServer(t)
	f.handlers["Email/get"] = func(json.RawMessage) (string, any) {
		return "Email/get", map[string]any{
			"accountId": "acc1", "state": "1",
			"list": []map[string]any{
				{"id": "e1", "textBody": []map[string]any{{"partId": "1"}},
					"bodyValues": map[string]any{"1": map[string]any{"value": "hello text"}}},
				{"id": "e2", "htmlBody": []map[string]any{{"partId": "2"}},
					"bodyValues": map[string]any{"2": map[string]any{"value": "<p>Hi <b>there</b></p>"}}},
			},
		}
	}
	c := f.client()
	out, err := c.FetchTextBodies(context.Background(), []string{"e1", "e2"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if out["e1"] != "hello text" {
		t.Errorf("e1 text = %q", out["e1"])
	}
	if out["e2"] != "Hi there" { // HTML stripped + whitespace collapsed
		t.Errorf("e2 stripped = %q", out["e2"])
	}
	if r, _ := c.FetchTextBodies(context.Background(), nil, 10); r != nil {
		t.Error("empty ids should return nil")
	}
}
