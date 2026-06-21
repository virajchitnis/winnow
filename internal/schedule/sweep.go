package schedule

import (
	"context"
	"fmt"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/jmap"
	"winnow/internal/store"
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

// ApplyReviewed files the mail you already previewed by applying the categories
// recorded in the decision log — no re-classification, no new LLM calls. It only
// touches preview (dry_run) decisions whose email is still in the inbox and not
// yet processed, applying each verbatim: low-confidence rows stay in the inbox
// (matching what the preview showed), others get their category's move/flag/
// mark-read behavior. It runs regardless of the global dry-run toggle (an
// explicit user action), holds the run lock, and returns how many were filed.
func (s *Scheduler) ApplyReviewed(ctx context.Context) (int, error) {
	select {
	case s.runLock <- struct{}{}:
		defer func() { <-s.runLock }()
	default:
		return 0, fmt.Errorf("a run is already in progress")
	}

	inbox, ok, err := s.mail.MailboxByRole(ctx, "inbox")
	if err != nil {
		return 0, fmt.Errorf("resolve inbox: %w", err)
	}
	if !ok {
		return 0, fmt.Errorf("no inbox mailbox found")
	}

	pending, err := s.store.PendingPreviewDecisions()
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}

	cats, err := s.store.Categories()
	if err != nil {
		return 0, err
	}
	catByName := map[string]store.Category{}
	for _, c := range cats {
		catByName[c.Name] = c
	}

	applied := 0
	for start := 0; start < len(pending); start += sweepChunkSize {
		if err := ctx.Err(); err != nil {
			return applied, err // graceful stop between chunks
		}
		end := start + sweepChunkSize
		if end > len(pending) {
			end = len(pending)
		}
		chunk := pending[start:end]

		ids := make([]string, len(chunk))
		for i, d := range chunk {
			ids[i] = d.EmailID
		}
		emails, err := s.mail.GetEmails(ctx, ids)
		if err != nil {
			return applied, fmt.Errorf("apply-reviewed get emails: %w", err)
		}
		byID := make(map[string]jmap.Email, len(emails))
		for _, e := range emails {
			byID[e.ID] = e
		}

		var (
			plans []actions.Plan
			emls  []jmap.Email
			rslts []classify.Result
			lows  []bool
		)
		for _, d := range chunk {
			e, ok := byID[d.EmailID]
			if !ok || !e.MailboxIDs[inbox.ID] {
				continue // gone from the inbox (or refiled already) — skip
			}
			if seen, _ := s.store.IsProcessed(e.ID); seen {
				continue
			}
			cat, ok := catByName[d.Category]
			if !ok {
				continue // category was deleted since the preview
			}
			plan := actions.Plan{EmailID: e.ID, Category: d.Category}
			if !d.LowConfidence {
				if cat.Moves() {
					plan.MoveTo = cat.DestinationFolder
					plan.MarkRead = cat.MarkRead
				}
				if cat.Flag {
					plan.Flag = true
				}
			}
			plans = append(plans, plan)
			emls = append(emls, e)
			rslts = append(rslts, classify.Result{
				Category: d.Category, Confidence: d.Confidence,
				Reason: d.Reason, Summary: d.Summary, UsedLLM: d.UsedLLM,
			})
			lows = append(lows, d.LowConfidence)
		}
		if len(plans) == 0 {
			continue
		}

		outcomes, applyErr := s.applier.Apply(ctx, plans, false)
		if applyErr != nil {
			s.log.Error("apply-reviewed apply failed", "err", applyErr)
		}
		for i, e := range emls {
			s.record(e, rslts[i], outcomes, i, lows[i], true)
			if i < len(outcomes) && outcomes[i].Action != actions.ActionError {
				applied++
			}
		}
		s.log.Info("apply-reviewed chunk done", "from", start, "to", end, "applied", applied)
	}
	return applied, nil
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
