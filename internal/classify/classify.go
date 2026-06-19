// Package classify decides which category an email belongs to, cheaply first.
//
// Order of resolution: sender allow/deny overrides, then confidently-known
// senders (from prior observations), then — only for the ambiguous remainder —
// Claude. When the LLM is unavailable (spend cap reached or an error), mail is
// left in the inbox at low confidence so nothing important is ever hidden.
package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"winnow/internal/config"
)

// Source records how a classification was reached.
type Source string

const (
	SourceAllow    Source = "allow"
	SourceDeny     Source = "deny"
	SourceKnown    Source = "known"
	SourceLLM      Source = "llm"
	SourceFallback Source = "fallback" // LLM unavailable → kept in inbox
)

// Mail is the minimal view of an email the classifier needs (mapped from JMAP
// by the caller, so this package has no JMAP dependency).
type Mail struct {
	ID                 string
	ThreadID           string
	Sender             string // lowercased address
	Domain             string // lowercased domain of Sender
	Subject            string
	Preview            string
	HasListUnsubscribe bool
	HasListID          bool
	Precedence         string
}

// CategoryInfo is the subset of a category the classifier reasons about.
type CategoryInfo struct {
	Name        string
	KeepInInbox bool
}

// Result is the classification of one Mail.
type Result struct {
	Category   string
	Confidence float64
	Reason     string
	Summary    string
	Source     Source
	UsedLLM    bool
}

// Lookups provides the cheap, free signals (implemented by the store).
type Lookups interface {
	// SenderOverride returns an allow/deny override for a sender or its domain.
	// important=true means an allow-list (→ keep in inbox); category is the
	// deny-list target when matched && !important.
	SenderOverride(sender, domain string) (category string, important bool, matched bool)
	// KnownCategory returns a confidently-known category for a sender/domain
	// (e.g. enough consistent prior observations), or ok=false.
	KnownCategory(sender, domain string) (category string, ok bool)
}

// importantCategory / attentionCategory name the inbox-retaining categories the
// heuristics and fallbacks steer toward. They must exist in the configured
// category set (they are seeded presets).
const (
	importantCategory = "Important"
	attentionCategory = "Needs attention"
)

// Classifier resolves categories for batches of mail.
type Classifier struct {
	anthropic *Anthropic
	lookups   Lookups
	maxTokens int
}

// New returns a Classifier.
func New(a *Anthropic, l Lookups) *Classifier {
	return &Classifier{anthropic: a, lookups: l, maxTokens: 2048}
}

// Request is a batch classification request.
type Request struct {
	Mails      []Mail
	Categories []CategoryInfo
	Model      string
	Privacy    config.PrivacyMode
	AllowLLM   bool // false when the spend cap is reached
}

// Classify returns a Result for each mail (same order). It never errors on
// individual LLM failures — those mails fall back to being kept in the inbox.
func (c *Classifier) Classify(ctx context.Context, req Request) ([]Result, error) {
	results := make([]Result, len(req.Mails))
	var llmIdx []int

	for i, m := range req.Mails {
		if cat, important, matched := c.lookups.SenderOverride(m.Sender, m.Domain); matched {
			if important {
				results[i] = Result{Category: importantCategory, Confidence: 1, Source: SourceAllow}
			} else {
				results[i] = Result{Category: cat, Confidence: 1, Source: SourceDeny}
			}
			continue
		}
		if cat, ok := c.lookups.KnownCategory(m.Sender, m.Domain); ok {
			results[i] = Result{Category: cat, Confidence: 0.95, Source: SourceKnown}
			continue
		}
		llmIdx = append(llmIdx, i)
	}

	if len(llmIdx) == 0 {
		return results, nil
	}

	if !req.AllowLLM {
		for _, i := range llmIdx {
			results[i] = fallbackResult()
		}
		return results, nil
	}

	batch := make([]Mail, len(llmIdx))
	for j, i := range llmIdx {
		batch[j] = req.Mails[i]
	}
	llmResults, err := c.classifyLLM(ctx, req.Model, req.Privacy, req.Categories, batch)
	if err != nil {
		// Whole-batch failure: keep everything in the inbox.
		for _, i := range llmIdx {
			results[i] = fallbackResult()
		}
		return results, err
	}
	for j, i := range llmIdx {
		results[i] = llmResults[j]
	}
	return results, nil
}

func fallbackResult() Result {
	return Result{
		Category:   attentionCategory,
		Confidence: 0,
		Reason:     "left in inbox (classifier unavailable)",
		Source:     SourceFallback,
	}
}

func (c *Classifier) classifyLLM(ctx context.Context, model string, privacy config.PrivacyMode, cats []CategoryInfo, mails []Mail) ([]Result, error) {
	system := buildSystemPrompt(cats)
	user := buildUserPrompt(privacy, mails)

	text, _, err := c.anthropic.Message(ctx, model, system, user, c.maxTokens)
	if err != nil {
		return nil, err
	}
	parsed, err := parseResults(text, len(mails))
	if err != nil {
		return nil, err
	}
	// Validate categories; unknown category → fall back to keep-in-inbox.
	valid := map[string]bool{}
	for _, ci := range cats {
		valid[ci.Name] = true
	}
	for i := range parsed {
		if !valid[parsed[i].Category] {
			parsed[i].Category = attentionCategory
			parsed[i].Confidence = 0
		}
		parsed[i].Source = SourceLLM
		parsed[i].UsedLLM = true
	}
	return parsed, nil
}

// llmItem is the JSON shape returned by the model, one per mail.
type llmItem struct {
	I          int     `json:"i"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	Summary    string  `json:"summary"`
}

func parseResults(text string, n int) ([]Result, error) {
	raw := extractJSONArray(text)
	if raw == "" {
		return nil, fmt.Errorf("classify: no JSON array in model output")
	}
	var items []llmItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("classify: decode model output: %w", err)
	}
	out := make([]Result, n)
	// Default any unmapped slot to a safe keep-in-inbox result.
	for i := range out {
		out[i] = Result{Category: attentionCategory, Confidence: 0, Source: SourceLLM, UsedLLM: true}
	}
	for _, it := range items {
		if it.I < 0 || it.I >= n {
			continue
		}
		conf := it.Confidence
		if conf < 0 {
			conf = 0
		} else if conf > 1 {
			conf = 1
		}
		out[it.I] = Result{
			Category:   it.Category,
			Confidence: conf,
			Reason:     it.Reason,
			Summary:    it.Summary,
		}
	}
	return out, nil
}

// extractJSONArray returns the substring from the first '[' to the matching
// last ']', tolerating prose around the JSON.
func extractJSONArray(s string) string {
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}
