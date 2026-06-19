package schedule

import (
	"strings"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/store"
)

// planFor turns a classification result + the resolved category into an action
// plan, applying the confidence gate.
//
// The gate is the false-negative safeguard: when an LLM/fallback result is
// below the threshold, the mail is KEPT in the inbox, unflagged, regardless of
// the guessed category. Allow/deny/known results carry high confidence and so
// are never gated.
func planFor(r classify.Result, cat store.Category, threshold float64) (plan actions.Plan, lowConfidence bool) {
	plan.Category = cat.Name

	lowConfidence = r.Confidence < threshold &&
		(r.Source == classify.SourceLLM || r.Source == classify.SourceFallback)
	if lowConfidence {
		// Keep in inbox, no move, no flag.
		return plan, true
	}

	if cat.Moves() {
		plan.MoveTo = cat.DestinationFolder
		plan.MarkRead = cat.MarkRead
	}
	if cat.Flag {
		plan.Flag = true
	}
	return plan, false
}

// domainOf returns the lowercased domain part of an email address, or "".
func domainOf(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return addr[at+1:]
}
