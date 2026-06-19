// Package actions applies classification decisions to Fastmail over JMAP:
// moving mail to a category's folder, flagging it, and/or marking it read.
// It never deletes, honors DRY_RUN, and retries transient failures.
package actions

import (
	"context"
	"sync"

	"winnow/internal/jmap"
	"winnow/internal/retry"
)

// JMAP is the subset of *jmap.Client the applier needs (mockable in tests).
type JMAP interface {
	EnsureMailbox(ctx context.Context, name string) (string, error)
	MailboxByRole(ctx context.Context, role string) (jmap.Mailbox, bool, error)
	UpdateEmails(ctx context.Context, updates []jmap.EmailUpdate) (map[string]string, error)
}

// Plan is the decided action for one email.
type Plan struct {
	EmailID  string
	Category string
	MoveTo   string // destination folder name; "" => keep in inbox
	Flag     bool
	MarkRead bool
}

// Action is the outcome label recorded for one plan.
type Action string

const (
	ActionMoved   Action = "moved"
	ActionFlagged Action = "flagged"
	ActionKept    Action = "kept"
	ActionDryRun  Action = "dry_run"
	ActionError   Action = "error"
)

// Result is the outcome of applying one plan.
type Result struct {
	EmailID string
	Action  Action
	Folder  string
	Err     string
}

// Applier applies plans, caching folder ids.
type Applier struct {
	j      JMAP
	policy retry.Policy

	mu       sync.Mutex
	folderID map[string]string // folder name -> mailbox id
}

// NewApplier returns an Applier.
func NewApplier(j JMAP) *Applier {
	return &Applier{j: j, policy: retry.DefaultPolicy, folderID: map[string]string{}}
}

// folder resolves (and caches) a destination folder name to a mailbox id.
func (a *Applier) folder(ctx context.Context, name string) (string, error) {
	a.mu.Lock()
	if id, ok := a.folderID[name]; ok {
		a.mu.Unlock()
		return id, nil
	}
	a.mu.Unlock()

	var id string
	err := retry.Do(ctx, a.policy, func() error {
		var e error
		id, e = a.j.EnsureMailbox(ctx, name)
		return e
	})
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.folderID[name] = id
	a.mu.Unlock()
	return id, nil
}

// Apply executes the given plans. In dry-run mode it resolves what would happen
// without mutating any mail. It returns one Result per plan, in order.
func (a *Applier) Apply(ctx context.Context, plans []Plan, dryRun bool) ([]Result, error) {
	results := make([]Result, len(plans))
	var updates []jmap.EmailUpdate
	idx := make([]int, 0, len(plans))

	for i, p := range plans {
		action, folderID, err := a.buildUpdate(ctx, p, &updates)
		if err != nil {
			results[i] = Result{EmailID: p.EmailID, Action: ActionError, Err: err.Error()}
			continue
		}
		results[i] = Result{EmailID: p.EmailID, Action: action, Folder: p.MoveTo}
		if dryRun {
			results[i].Action = ActionDryRun
			continue
		}
		if folderID != "" || p.Flag || p.MarkRead {
			idx = append(idx, i)
		}
	}

	if dryRun || len(updates) == 0 {
		return results, nil
	}

	var notUpdated map[string]string
	err := retry.Do(ctx, a.policy, func() error {
		var e error
		notUpdated, e = a.j.UpdateEmails(ctx, updates)
		return e
	})
	if err != nil {
		// Whole batch failed: mark the affected results as errors.
		for _, i := range idx {
			results[i].Action = ActionError
			results[i].Err = err.Error()
		}
		return results, err
	}
	for _, i := range idx {
		if reason, bad := notUpdated[results[i].EmailID]; bad {
			results[i].Action = ActionError
			results[i].Err = reason
		}
	}
	return results, nil
}

// buildUpdate computes the action + JMAP patch for a plan, appending to updates
// when there is something to change. It returns the intended action and (when
// moving) the resolved folder id.
func (a *Applier) buildUpdate(ctx context.Context, p Plan, updates *[]jmap.EmailUpdate) (Action, string, error) {
	upd := jmap.EmailUpdate{ID: p.EmailID}
	action := ActionKept
	var folderID string

	if p.MoveTo != "" {
		id, err := a.folder(ctx, p.MoveTo)
		if err != nil {
			return ActionError, "", err
		}
		folderID = id
		upd.MailboxIDs = map[string]bool{id: true}
		action = ActionMoved
	}

	kw := map[string]bool{}
	if p.Flag {
		kw["$flagged"] = true
		if action == ActionKept {
			action = ActionFlagged
		}
	}
	if p.MarkRead {
		kw["$seen"] = true
	}
	if len(kw) > 0 {
		upd.SetKeywords = kw
	}

	if upd.MailboxIDs != nil || upd.SetKeywords != nil {
		*updates = append(*updates, upd)
	}
	return action, folderID, nil
}
