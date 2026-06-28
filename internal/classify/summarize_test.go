package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSummarizeNewsletters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 50},
			"content": []map[string]any{{
				"type": "text",
				"text": "Here you go:\n[\"Summary one.\", \"Summary two.\"]",
			}},
		})
	}))
	defer srv.Close()

	a := NewAnthropic("k", WithBaseURL(srv.URL))
	out, err := a.SummarizeNewsletters(context.Background(), "m", []NewsletterInput{
		{Sender: "a@x.com", Subject: "One", Body: "..."},
		{Sender: "b@x.com", Subject: "Two", Body: "..."},
	}, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0] != "Summary one." || out[1] != "Summary two." {
		t.Fatalf("summaries = %#v", out)
	}
}

func TestParseStringArrayTolerant(t *testing.T) {
	if got := parseStringArray("noise ```json\n[\"a\",\"b\"]\n``` trailer"); len(got) != 2 || got[1] != "b" {
		t.Errorf("parseStringArray = %#v", got)
	}
	if got := parseStringArray("no array here"); got != nil {
		t.Errorf("expected nil, got %#v", got)
	}
}
