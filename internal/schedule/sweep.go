package schedule

import (
	"context"
	"fmt"

	"winnow/internal/actions"
	"winnow/internal/jmap"
)

const (
	sweepChunkSize = 75
	sweepMaxEmails = 10000
)

// SweepResult summarizes an initial-sweep run.
type SweepResult struct {
	Considered int
	Processed  int
	Applied    bool
}

// Refile moves a single email into the given category right now, applying that
// category's folder/flag/mark-read behavior over JMAP regardless of dry-run —
// an explicit, per-email correction from the dashboard. It holds the run lock
// so it can't race triage/sweep, marks the email processed, and returns the
// resulting action label ("moved", "flagged", or "kept").
func (s *Scheduler) Refile(ctx context.Context, emailID, category string) (string, error) {
	select {
	case s.runLock <- struct{}{}:
		defer func() { <-s.runLock }()
	default:
		return "", fmt.Errorf("a run is already in progress")
	}

	cat, ok, err := s.store.CategoryByName(category)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("unknown category %q", category)
	}

	plan := actions.Plan{EmailID: emailID, Category: cat.Name}
	if cat.Moves() {
		plan.MoveTo = cat.DestinationFolder
		plan.MarkRead = cat.MarkRead
	} else if inbox, ok, ierr := s.mail.MailboxByRole(ctx, "inbox"); ierr == nil && ok {
		// Keep-in-inbox category: ensure the mail is back in the inbox.
		plan.MoveTo = inbox.Name
	}
	if cat.Flag {
		plan.Flag = true
	}

	results, err := s.applier.Apply(ctx, []actions.Plan{plan}, false)
	if err != nil {
		return "", err
	}
	res := results[0]
	if res.Action == actions.ActionError {
		return "", fmt.Errorf("refile failed: %s", res.Err)
	}
	_ = s.store.MarkProcessed(emailID)
	return string(res.Action), nil
}

// Sweep processes the existing inbox backlog in checkpointed chunks. When
// apply is false it runs as a dry-run preview (recording proposed decisions
// without moving anything or marking mail processed); when true it files mail
// chunk-by-chunk. It holds the single-flight run lock and respects context
// cancellation between chunks (so SIGTERM stops cleanly after a chunk).
func (s *Scheduler) Sweep(ctx context.Context, apply bool) (SweepResult, error) {
	select {
	case s.runLock <- struct{}{}:
		defer func() { <-s.runLock }()
	default:
		return SweepResult{}, fmt.Errorf("a run is already in progress")
	}

	settings, err := s.store.LoadSettings(s.defaults)
	if err != nil {
		return SweepResult{}, err
	}
	settings.DryRun = !apply // preview unless applying

	inbox, ok, err := s.mail.MailboxByRole(ctx, "inbox")
	if err != nil {
		return SweepResult{}, err
	}
	if !ok {
		return SweepResult{}, fmt.Errorf("no inbox mailbox found")
	}

	ids, err := s.mail.QueryInbox(ctx, inbox.ID, sweepMaxEmails)
	if err != nil {
		return SweepResult{}, err
	}

	res := SweepResult{Applied: apply}
	for start := 0; start < len(ids); start += sweepChunkSize {
		if err := ctx.Err(); err != nil {
			return res, err // graceful stop between chunks
		}
		end := start + sweepChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]

		emails, err := s.mail.GetEmails(ctx, chunk)
		if err != nil {
			return res, fmt.Errorf("sweep get emails: %w", err)
		}
		var todo []jmap.Email
		for _, e := range emails {
			if !e.MailboxIDs[inbox.ID] {
				continue
			}
			seen, err := s.store.IsProcessed(e.ID)
			if err != nil {
				return res, err
			}
			if !seen {
				todo = append(todo, e)
			}
		}
		res.Considered += len(todo)

		// Only mark processed when actually applying.
		if err := s.process(ctx, settings, todo, apply); err != nil {
			return res, err
		}
		res.Processed += len(todo)
		s.log.Info("sweep chunk done", "from", start, "to", end, "of", len(ids), "apply", apply)
	}
	return res, nil
}
