package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComposeBriefing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 50},
			"content": []map[string]any{{
				"type": "text",
				"text": `Here is your briefing:
[{"heading":"Tech & AI","items":["A new model shipped (The Verge).","Chip supply easing."]},
 {"heading":"Markets","items":["Rates held steady."]}]`,
			}},
		})
	}))
	defer srv.Close()

	a := NewAnthropic("k", WithBaseURL(srv.URL))
	secs, err := a.ComposeBriefing(context.Background(), "m", []NewsletterInput{
		{Sender: "a@x.com", Subject: "One", Body: "..."},
		{Sender: "b@x.com", Subject: "Two", Body: "..."},
	}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 2 || secs[0].Heading != "Tech & AI" || len(secs[0].Items) != 2 {
		t.Fatalf("sections = %#v", secs)
	}
	if secs[1].Heading != "Markets" || secs[1].Items[0] != "Rates held steady." {
		t.Errorf("second section = %#v", secs[1])
	}
}

func TestParseSectionsTolerant(t *testing.T) {
	got := parseSections("noise ```json\n[{\"heading\":\"H\",\"items\":[\"a\",\"\"]}]\n``` trailer")
	if len(got) != 1 || got[0].Heading != "H" || len(got[0].Items) != 1 {
		t.Errorf("parseSections = %#v", got)
	}
	if parseSections("no json") != nil {
		t.Error("expected nil for non-JSON")
	}
	// Heading-less or empty-items sections are dropped.
	if got := parseSections(`[{"heading":"","items":["x"]},{"heading":"Y","items":[]}]`); got != nil {
		t.Errorf("expected empty, got %#v", got)
	}
}
