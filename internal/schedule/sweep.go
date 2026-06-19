package schedule

import (
	"context"
	"fmt"

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
