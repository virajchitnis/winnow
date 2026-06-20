package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"winnow/internal/config"
)

// fakeLookups implements Lookups for tests.
type fakeLookups struct {
	overrides map[string]struct {
		cat       string
		important bool
	}
	known map[string]string
}

func (f fakeLookups) SenderOverride(sender, domain string) (string, bool, bool) {
	if o, ok := f.overrides[sender]; ok {
		return o.cat, o.important, true
	}
	if o, ok := f.overrides["@"+domain]; ok {
		return o.cat, o.important, true
	}
	return "", false, false
}

func (f fakeLookups) KnownCategory(sender, domain string) (string, bool) {
	if c, ok := f.known[sender]; ok {
		return c, true
	}
	if c, ok := f.known["@"+domain]; ok {
		return c, true
	}
	return "", false
}

var testCats = []CategoryInfo{
	{Name: "Important", KeepInInbox: true},
	{Name: "Needs attention", KeepInInbox: true},
	{Name: "Promotional"},
	{Name: "Social"},
	{Name: "Newsletters"},
}

func TestExtractJSONArray(t *testing.T) {
	cases := map[string]string{
		`[{"i":0}]`:                  `[{"i":0}]`,
		"prose [{\"i\":0}] trailing": `[{"i":0}]`,
		"no array here":              "",
	}
	for in, want := range cases {
		if got := extractJSONArray(in); got != want {
			t.Errorf("extractJSONArray(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseResultsClampAndIndex(t *testing.T) {
	text := `Here you go: [{"i":1,"category":"Promotional","confidence":1.7,"reason":"r","summary":"s"},{"i":0,"category":"Social","confidence":-0.5}]`
	out, err := parseResults(text, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Category != "Social" || out[0].Confidence != 0 {
		t.Errorf("idx0 = %+v", out[0])
	}
	if out[1].Category != "Promotional" || out[1].Confidence != 1 {
		t.Errorf("idx1 = %+v (confidence should clamp to 1)", out[1])
	}
}

func TestClassifyHeuristics(t *testing.T) {
	lk := fakeLookups{
		overrides: map[string]struct {
			cat       string
			important bool
		}{
			"boss@work.example": {important: true},
			"@spam.example":     {cat: "Promotional"},
		},
		known: map[string]string{"@retailer.example": "Promotional"},
	}
	c := New(nil, lk) // nil Anthropic: heuristics must not call it
	req := Request{
		Categories: testCats,
		AllowLLM:   true,
		Mails: []Mail{
			{Sender: "boss@work.example", Domain: "work.example"},
			{Sender: "x@spam.example", Domain: "spam.example"},
			{Sender: "deals@retailer.example", Domain: "retailer.example"},
		},
	}
	out, err := c.Classify(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Source != SourceAllow || out[0].Category != "Important" {
		t.Errorf("allow override: %+v", out[0])
	}
	if out[1].Source != SourceDeny || out[1].Category != "Promotional" {
		t.Errorf("deny override: %+v", out[1])
	}
	if out[2].Source != SourceKnown || out[2].Category != "Promotional" {
		t.Errorf("known sender: %+v", out[2])
	}
}

func TestClassifyFallbackWhenLLMDisallowed(t *testing.T) {
	c := New(nil, fakeLookups{})
	out, err := c.Classify(context.Background(), Request{
		Categories: testCats,
		AllowLLM:   false,
		Mails:      []Mail{{Sender: "a@b.example", Domain: "b.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Source != SourceFallback || out[0].Confidence != 0 {
		t.Errorf("expected fallback keep-in-inbox: %+v", out[0])
	}
}

func TestClassifyLLMPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing auth/version headers")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		// System must be a cacheable block.
		sys, _ := body["system"].([]any)
		if len(sys) == 0 {
			t.Errorf("system not sent as block array")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 100, "cache_read_input_tokens": 50},
			"content": []map[string]any{{
				"type": "text",
				"text": `[{"i":0,"category":"Promotional","confidence":0.9,"reason":"sale","summary":"40% off"}]`,
			}},
		})
	}))
	defer srv.Close()

	a := NewAnthropic("k", WithBaseURL(srv.URL))
	c := New(a, fakeLookups{})
	out, err := c.Classify(context.Background(), Request{
		Categories: testCats,
		Model:      "claude-haiku-4-5",
		Privacy:    config.PrivacySnippet,
		AllowLLM:   true,
		Mails:      []Mail{{Sender: "deals@x.example", Domain: "x.example", Subject: "Sale", Preview: "40% off", HasListUnsubscribe: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Category != "Promotional" || out[0].Confidence != 0.9 || !out[0].UsedLLM {
		t.Errorf("llm result = %+v", out[0])
	}
}

func TestClassifyLLMUnknownCategoryFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"content": []map[string]any{{
				"type": "text",
				"text": `[{"i":0,"category":"Nonexistent","confidence":0.99}]`,
			}},
		})
	}))
	defer srv.Close()
	a := NewAnthropic("k", WithBaseURL(srv.URL))
	c := New(a, fakeLookups{})
	out, err := c.Classify(context.Background(), Request{
		Categories: testCats, Model: "m", AllowLLM: true,
		Mails: []Mail{{Sender: "a@b.example", Domain: "b.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Category != "Needs attention" || out[0].Confidence != 0 {
		t.Errorf("unknown category should fall back to keep-in-inbox: %+v", out[0])
	}
}

func TestBuildUserPromptPrivacy(t *testing.T) {
	mails := []Mail{{Sender: "a@b.example", Subject: "Hi", Preview: "secret body"}}
	withSnippet := buildUserPrompt(config.PrivacySnippet, mails)
	if !strings.Contains(withSnippet, "secret body") {
		t.Error("snippet mode should include preview")
	}
	noSnippet := buildUserPrompt(config.PrivacySubjectSender, mails)
	if strings.Contains(noSnippet, "secret body") {
		t.Error("subject_sender mode must NOT include preview")
	}
}
