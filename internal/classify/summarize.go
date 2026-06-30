package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NewsletterInput is one newsletter fed into the briefing composer.
type NewsletterInput struct {
	Sender  string
	Subject string
	Body    string
}

// BriefingSection is one themed group of the composed personal newsletter.
type BriefingSection struct {
	Heading string   `json:"heading"`
	Items   []string `json:"items"`
}

const composeSystem = "You are the editor of the reader's personal morning newsletter. " +
	"Below are the full texts of the newsletters they received today. Synthesize them into " +
	"ONE cohesive briefing written FOR the reader. Pull together the most useful, interesting, " +
	"and notable information ACROSS ALL of them — actual news, updates, data, insights, ideas, " +
	"and noteworthy deals — not just headlines. Merge overlapping coverage. Group related items " +
	"under a few short thematic headings (e.g. \"Tech & AI\", \"Markets & business\", " +
	"\"Around the web\"). Under each heading give a handful of concise but substantive bullets " +
	"with specifics; where useful, cite the source newsletter in parentheses. Ignore ads, " +
	"boilerplate, tracking, and unsubscribe footers. Aim for a satisfying, skimmable read. " +
	"Reply with ONLY a JSON array of objects, each {\"heading\": string, \"items\": [string,...]}."

// ComposeBriefing synthesizes the newsletters into a single themed briefing in
// one batched Claude call. Returns the sections in order; a missing/unparseable
// reply yields nil rather than an error so the morning briefing still sends.
func (a *Anthropic) ComposeBriefing(ctx context.Context, model string, items []NewsletterInput, maxTokens int) ([]BriefingSection, error) {
	if len(items) == 0 {
		return nil, nil
	}
	var b strings.Builder
	for i, it := range items {
		fmt.Fprintf(&b, "=== Newsletter %d — %s — %s ===\n%s\n\n", i+1, it.Sender, it.Subject, it.Body)
	}
	text, _, err := a.Message(ctx, model, composeSystem, b.String(), maxTokens)
	if err != nil {
		return nil, err
	}
	return parseSections(text), nil
}

// parseSections extracts the JSON array of sections from a model reply,
// tolerating surrounding prose or code fences. Sections with no items are
// dropped.
func parseSections(s string) []BriefingSection {
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end <= start {
		return nil
	}
	var raw []BriefingSection
	if err := json.Unmarshal([]byte(s[start:end+1]), &raw); err != nil {
		return nil
	}
	out := make([]BriefingSection, 0, len(raw))
	for _, sec := range raw {
		var items []string
		for _, it := range sec.Items {
			if strings.TrimSpace(it) != "" {
				items = append(items, strings.TrimSpace(it))
			}
		}
		if sec.Heading == "" || len(items) == 0 {
			continue
		}
		out = append(out, BriefingSection{Heading: strings.TrimSpace(sec.Heading), Items: items})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
