package classify

import (
	"fmt"
	"strings"

	"winnow/internal/config"
)

// buildSystemPrompt builds the stable, cacheable classification prompt. It must
// not contain any per-request data — the category set is stable within a run,
// so the whole prompt is reused from cache across calls in the 5-minute window.
func buildSystemPrompt(cats []CategoryInfo) string {
	var b strings.Builder
	b.WriteString(`You are an email triage classifier. You assign each incoming email to exactly one category from the list below, based on the sender, subject, and a short preview snippet.

Guidance:
- Be decisive for clearly promotional, social, or newsletter mail.
- A personal message, a reply, a bill or statement, a security or account notice, an appointment, or anything time-sensitive belongs in an inbox-retaining category.
- When you are genuinely unsure, PREFER an inbox-retaining category and report a LOW confidence. It is far worse to hide an important email than to leave a promo in the inbox.
- "confidence" is your probability (0.0–1.0) that the category is correct.

Categories:
`)
	for _, ci := range cats {
		tag := "filed to a folder"
		if ci.KeepInInbox {
			tag = "kept in the inbox"
		}
		fmt.Fprintf(&b, "- %s (%s)\n", ci.Name, tag)
	}

	b.WriteString(`
Respond with ONLY a JSON array, one object per email, in input order:
[{"i": <index>, "category": "<exact category name>", "confidence": <0.0-1.0>, "reason": "<short>", "summary": "<one-line summary>"}]

Examples of the reasoning (not literal categories — use the list above):
- "48 hours only — 40% off everything" from deals@store.example with an unsubscribe link → promotional, high confidence.
- "Maya commented on your file" from notifications@social.example → social/activity, high confidence.
- "Your December statement is ready" from alerts@bank.example → inbox-retaining (account notice), high confidence.
- "Re: lunch tomorrow?" from a personal address → inbox-retaining, high confidence.
- An ambiguous one-off from an unknown sender with no clear signal → inbox-retaining, LOW confidence.
`)
	return b.String()
}

// buildUserPrompt renders the batch of mail per the privacy mode. In
// subject_sender mode no body snippet is included.
func buildUserPrompt(privacy config.PrivacyMode, mails []Mail) string {
	var b strings.Builder
	b.WriteString("Classify these emails:\n\n")
	for i, m := range mails {
		fmt.Fprintf(&b, "[%d]\n", i)
		fmt.Fprintf(&b, "from: %s\n", m.Sender)
		fmt.Fprintf(&b, "subject: %s\n", oneLine(m.Subject))
		signals := mailSignals(m)
		if signals != "" {
			fmt.Fprintf(&b, "signals: %s\n", signals)
		}
		if privacy == config.PrivacySnippet && m.Preview != "" {
			fmt.Fprintf(&b, "preview: %s\n", oneLine(truncate(m.Preview, 280)))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func mailSignals(m Mail) string {
	var s []string
	if m.HasListUnsubscribe {
		s = append(s, "has-unsubscribe")
	}
	if m.HasListID {
		s = append(s, "mailing-list")
	}
	if p := strings.ToLower(strings.TrimSpace(m.Precedence)); p == "bulk" || p == "list" {
		s = append(s, "precedence-bulk")
	}
	return strings.Join(s, ", ")
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
