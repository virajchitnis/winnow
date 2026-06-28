package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NewsletterInput is one newsletter to summarize for the briefing.
type NewsletterInput struct {
	Sender  string
	Subject string
	Body    string
}

const summarizeSystem = "You summarize email newsletters for a personal daily briefing. " +
	"For each newsletter, write 1–2 plain sentences capturing the most useful or notable " +
	"content (headlines, key updates, deals worth knowing) — no preamble, no marketing fluff. " +
	"Reply with ONLY a JSON array of strings, one summary per newsletter, in the same order."

// SummarizeNewsletters returns a one-to-two sentence summary for each input, in
// order, in a single batched Claude call. Used by the opt-in newsletter section
// of the morning briefing. A missing/short reply yields empty entries rather
// than an error so the briefing still sends.
func (a *Anthropic) SummarizeNewsletters(ctx context.Context, model string, items []NewsletterInput, maxTokens int) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	var b strings.Builder
	for i, it := range items {
		fmt.Fprintf(&b, "[%d] From: %s\nSubject: %s\n%s\n\n", i, it.Sender, it.Subject, it.Body)
	}
	text, _, err := a.Message(ctx, model, summarizeSystem, b.String(), maxTokens)
	if err != nil {
		return nil, err
	}
	arr := parseStringArray(text)
	out := make([]string, len(items))
	for i := range items {
		if i < len(arr) {
			out[i] = strings.TrimSpace(arr[i])
		}
	}
	return out, nil
}

// parseStringArray extracts a JSON array of strings from a model reply,
// tolerating surrounding prose or code fences.
func parseStringArray(s string) []string {
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end <= start {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(s[start:end+1]), &arr); err != nil {
		return nil
	}
	return arr
}
