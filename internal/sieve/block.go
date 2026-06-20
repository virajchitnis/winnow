// Package sieve generates the managed-block Fastmail Sieve rules from approved
// sender→category candidates and splices them into the user's active script
// WITHOUT touching anything the user wrote.
package sieve

import (
	"fmt"
	"sort"
	"strings"
)

// Managed-block delimiters. Winnow owns only the text between these markers.
const (
	StartMarker = "# >>> winnow (managed) >>>"
	EndMarker   = "# <<< winnow (managed) <<<"
)

// CategoryRule maps a category's destination folder to the domains that should
// be filed there server-side.
type CategoryRule struct {
	Category string
	Folder   string
	Domains  []string
}

// BuildManagedBlock renders the managed block: one consolidated rule per
// category, each matching a sorted domain list. Output is deterministic.
func BuildManagedBlock(rules []CategoryRule) string {
	var b strings.Builder
	b.WriteString(StartMarker)
	b.WriteString("\n# Managed by Winnow — do not edit by hand. Edit categories/rules in the dashboard.\n")

	// Sort categories for determinism.
	sorted := append([]CategoryRule(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Category < sorted[j].Category })

	for _, r := range sorted {
		if r.Folder == "" || len(r.Domains) == 0 {
			continue
		}
		domains := append([]string(nil), r.Domains...)
		sort.Strings(domains)
		quoted := make([]string, 0, len(domains))
		for _, d := range domains {
			quoted = append(quoted, quote(d))
		}
		fmt.Fprintf(&b, "# %s\n", r.Category)
		fmt.Fprintf(&b, "if address :domain :is \"from\" [%s] {\n", strings.Join(quoted, ", "))
		fmt.Fprintf(&b, "\tfileinto %s;\n\tstop;\n}\n", quote(r.Folder))
	}

	b.WriteString(EndMarker)
	return b.String()
}

// Splice inserts or replaces the managed block in an existing script, leaving
// everything the user wrote byte-for-byte intact.
//
//   - If a managed block already exists, only the text between the markers
//     (inclusive) is replaced.
//   - Otherwise the block is appended at the end (so the user's rules run
//     first), separated by a blank line.
func Splice(existing, block string) string {
	start := strings.Index(existing, StartMarker)
	if start < 0 {
		// Append.
		if strings.TrimSpace(existing) == "" {
			return block + "\n"
		}
		sep := "\n"
		if !strings.HasSuffix(existing, "\n") {
			sep = "\n\n"
		} else if !strings.HasSuffix(existing, "\n\n") {
			sep = "\n"
		}
		return existing + sep + block + "\n"
	}

	endIdx := strings.Index(existing, EndMarker)
	if endIdx < 0 || endIdx < start {
		// Corrupt/half-present markers: append a fresh block rather than guess.
		return existing + "\n" + block + "\n"
	}
	end := endIdx + len(EndMarker)
	return existing[:start] + block + existing[end:]
}

// ExtractManagedBlock returns the current managed block (inclusive of markers),
// or "" if none is present.
func ExtractManagedBlock(script string) string {
	start := strings.Index(script, StartMarker)
	if start < 0 {
		return ""
	}
	endIdx := strings.Index(script, EndMarker)
	if endIdx < 0 || endIdx < start {
		return ""
	}
	return script[start : endIdx+len(EndMarker)]
}

// quote returns a Sieve-quoted string.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
