package digest

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"winnow/internal/actions"
	"winnow/internal/store"
)

// Compose builds the digest subject and plain-text body from the day's
// decisions and any active errors. Pure and deterministic for testing.
func Compose(decisions []store.Decision, errs []store.AppError, now time.Time) (subject, body string) {
	date := now.Format("Mon Jan 2")
	subject = fmt.Sprintf("Winnow digest — %s", date)

	var moved, kept, flagged, lowConf int
	perCategory := map[string]int{}
	var important, review []store.Decision
	for _, d := range decisions {
		switch d.Action {
		case string(actions.ActionMoved):
			moved++
			perCategory[d.Category]++
		case string(actions.ActionFlagged):
			flagged++
			important = append(important, d)
		default:
			kept++
		}
		if d.LowConfidence {
			lowConf++
			review = append(review, d)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Winnow digest for %s\n\n", date)
	fmt.Fprintf(&b, "Processed %d emails in the last 24 hours:\n", len(decisions))
	fmt.Fprintf(&b, "  • %d filed to folders\n", moved)
	fmt.Fprintf(&b, "  • %d flagged in your inbox\n", flagged)
	fmt.Fprintf(&b, "  • %d kept in your inbox\n", kept)
	if lowConf > 0 {
		fmt.Fprintf(&b, "  • %d kept for review (low confidence)\n", lowConf)
	}

	if len(perCategory) > 0 {
		b.WriteString("\nFiled by category:\n")
		for _, kv := range sortedCounts(perCategory) {
			fmt.Fprintf(&b, "  • %s: %d\n", kv.name, kv.n)
		}
	}

	if len(important) > 0 {
		b.WriteString("\nFlagged for your attention:\n")
		for _, d := range important {
			writeItem(&b, d)
		}
	}

	if len(review) > 0 {
		b.WriteString("\nKept in inbox — worth a quick look (low confidence):\n")
		for _, d := range review {
			writeItem(&b, d)
		}
	}

	if len(errs) > 0 {
		b.WriteString("\n⚠ Heads up — Winnow hit some errors:\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "  • [%s] %s\n", e.Kind, oneLine(e.Message))
		}
	}

	b.WriteString("\n—\nThis digest is also Winnow's heartbeat: if it stops arriving, check that the service is running.\n")
	return subject, b.String()
}

func writeItem(b *strings.Builder, d store.Decision) {
	summary := d.Summary
	if summary == "" {
		summary = oneLine(d.Subject)
	}
	from := d.Sender
	if from == "" {
		from = "(unknown sender)"
	}
	fmt.Fprintf(b, "  • %s — %s\n", from, oneLine(summary))
}

type countKV struct {
	name string
	n    int
}

func sortedCounts(m map[string]int) []countKV {
	out := make([]countKV, 0, len(m))
	for k, v := range m {
		out = append(out, countKV{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].name < out[j].name
	})
	return out
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
