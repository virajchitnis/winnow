package sieve

import (
	"context"
	"fmt"
	"sort"

	"winnow/internal/jmap"
	"winnow/internal/store"
)

// DefaultByteBudget caps the managed block size, well under Fastmail's script
// limit, leaving the user's rule budget untouched.
const DefaultByteBudget = 32 * 1024

// sieveScriptName is the name used when Winnow has to create a script (no active
// script exists yet).
const sieveScriptName = "winnow"

// JMAP is the Sieve surface the generator needs (mockable in tests).
type JMAP interface {
	ActiveSieveScript(ctx context.Context) (jmap.SieveScript, string, bool, error)
	ValidateSieve(ctx context.Context, content string) error
	PutActiveSieve(ctx context.Context, name, content, existingID string) (string, error)
}

// Store is the persistence surface the generator needs.
type Store interface {
	SieveCandidates(status string) ([]store.SieveCandidate, error)
	CategoryByName(name string) (store.Category, bool, error)
	BackupSieve(content string) error
	LatestSieveBackup() (string, bool, error)
}

// Generator builds and applies the managed Sieve block.
type Generator struct {
	jmap   JMAP
	store  Store
	budget int
}

// New returns a Generator.
func New(j JMAP, s Store) *Generator {
	return &Generator{jmap: j, store: s, budget: DefaultByteBudget}
}

// SetBudget overrides the managed-block byte budget.
func (g *Generator) SetBudget(n int) {
	if n > 0 {
		g.budget = n
	}
}

// Rules builds the CategoryRules from approved candidates (pruned to budget).
// Returned for dashboard preview as well as for Apply.
func (g *Generator) Rules() ([]CategoryRule, error) {
	cands, err := g.store.SieveCandidates(store.SieveApproved)
	if err != nil {
		return nil, err
	}

	// Group domains by category, remembering observation counts for pruning.
	type entry struct {
		category string
		domain   string
		obs      int
	}
	folderFor := map[string]string{}
	var entries []entry
	for _, c := range cands {
		folder, ok := folderFor[c.Category]
		if !ok {
			cat, found, err := g.store.CategoryByName(c.Category)
			if err != nil {
				return nil, err
			}
			if !found || !cat.Moves() {
				folderFor[c.Category] = "" // category no longer files to a folder
				continue
			}
			folder = cat.DestinationFolder
			folderFor[c.Category] = folder
		}
		if folder == "" {
			continue
		}
		entries = append(entries, entry{c.Category, c.Domain, c.Observations})
	}

	// Greedily include highest-observation domains until the rendered block
	// would exceed the byte budget; pruned domains fall back to the runtime
	// LLM mover.
	sort.Slice(entries, func(i, j int) bool { return entries[i].obs > entries[j].obs })
	byCat := map[string][]string{}
	for _, e := range entries {
		byCat[e.category] = append(byCat[e.category], e.domain)
		if len(BuildManagedBlock(rulesFromMap(byCat, folderFor))) > g.budget {
			// Undo the last addition.
			byCat[e.category] = byCat[e.category][:len(byCat[e.category])-1]
		}
	}
	return rulesFromMap(byCat, folderFor), nil
}

func rulesFromMap(byCat map[string][]string, folderFor map[string]string) []CategoryRule {
	var rules []CategoryRule
	for cat, domains := range byCat {
		if len(domains) == 0 {
			continue
		}
		rules = append(rules, CategoryRule{Category: cat, Folder: folderFor[cat], Domains: domains})
	}
	return rules
}

// Preview returns the managed block that Apply would write, without applying it.
func (g *Generator) Preview() (string, error) {
	rules, err := g.Rules()
	if err != nil {
		return "", err
	}
	return BuildManagedBlock(rules), nil
}

// Apply regenerates the managed block and writes it into the active Sieve
// script. The user's rules are preserved byte-for-byte; the spliced script is
// validated before activation; and the prior script is backed up first.
func (g *Generator) Apply(ctx context.Context) error {
	rules, err := g.Rules()
	if err != nil {
		return err
	}
	block := BuildManagedBlock(rules)

	script, content, exists, err := g.jmap.ActiveSieveScript(ctx)
	if err != nil {
		return err
	}
	existingID := ""
	name := sieveScriptName
	if exists {
		existingID = script.ID
		name = script.Name
	}

	newScript := Splice(content, block)
	if newScript == content {
		return nil // nothing changed
	}

	if err := g.jmap.ValidateSieve(ctx, newScript); err != nil {
		return fmt.Errorf("refusing to activate invalid script: %w", err)
	}
	if exists {
		if err := g.store.BackupSieve(content); err != nil {
			return err
		}
	}
	if _, err := g.jmap.PutActiveSieve(ctx, name, newScript, existingID); err != nil {
		return err
	}
	return nil
}

// Revert restores the most recent backup as the active script.
func (g *Generator) Revert(ctx context.Context) error {
	backup, ok, err := g.store.LatestSieveBackup()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no Sieve backup to revert to")
	}
	if err := g.jmap.ValidateSieve(ctx, backup); err != nil {
		return fmt.Errorf("backup failed validation: %w", err)
	}
	script, _, exists, err := g.jmap.ActiveSieveScript(ctx)
	if err != nil {
		return err
	}
	existingID, name := "", sieveScriptName
	if exists {
		existingID, name = script.ID, script.Name
	}
	_, err = g.jmap.PutActiveSieve(ctx, name, backup, existingID)
	return err
}
