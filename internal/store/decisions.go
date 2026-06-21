package store

// Decision is one classification outcome recorded in the log.
type Decision struct {
	ID            int64
	EmailID       string
	ThreadID      string
	Sender        string
	Subject       string
	Category      string
	Confidence    float64
	Reason        string
	Summary       string
	Action        string // moved | kept | flagged | dry_run | error
	LowConfidence bool
	UsedLLM       bool
	CreatedAt     string
}

// RecordDecision appends a decision to the log. To keep previews idempotent it
// holds at most one preview (dry_run) decision per email: recording any new
// non-error decision for an email first clears its prior preview row, so
// re-running a dry-run sweep replaces previews instead of piling up duplicates,
// and a later real decision supersedes the preview cleanly.
func (s *Store) RecordDecision(d Decision) error {
	if d.Action != "error" {
		if _, err := s.db.Exec(
			"DELETE FROM decisions WHERE email_id = ? AND action = 'dry_run'", d.EmailID); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`
		INSERT INTO decisions(email_id, thread_id, sender, subject, category, confidence,
			reason, summary, action, low_confidence, used_llm, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.EmailID, d.ThreadID, d.Sender, d.Subject, d.Category, d.Confidence,
		d.Reason, d.Summary, d.Action, boolToInt(d.LowConfidence), boolToInt(d.UsedLLM), s.nowStr())
	return err
}

// RecentDecisions returns the most recent decisions, newest first.
func (s *Store) RecentDecisions(limit int) ([]Decision, error) {
	rows, err := s.db.Query(`
		SELECT id, email_id, thread_id, sender, subject, category, confidence,
			reason, summary, action, low_confidence, used_llm, created_at
		FROM decisions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDecisions(rows)
}

// DecisionsPage returns decisions newest-first with limit/offset, for paging
// through the full history in the Review tab.
func (s *Store) DecisionsPage(limit, offset int) ([]Decision, error) {
	rows, err := s.db.Query(`
		SELECT id, email_id, thread_id, sender, subject, category, confidence,
			reason, summary, action, low_confidence, used_llm, created_at
		FROM decisions ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDecisions(rows)
}

// DecisionsSince returns decisions recorded at/after the given RFC3339 cutoff,
// newest first — used to build the daily digest.
func (s *Store) DecisionsSince(cutoff string) ([]Decision, error) {
	rows, err := s.db.Query(`
		SELECT id, email_id, thread_id, sender, subject, category, confidence,
			reason, summary, action, low_confidence, used_llm, created_at
		FROM decisions WHERE created_at >= ? ORDER BY id DESC`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDecisions(rows)
}

// PruneDecisions deletes decisions older than the given RFC3339 cutoff.
func (s *Store) PruneDecisions(cutoff string) (int64, error) {
	res, err := s.db.Exec("DELETE FROM decisions WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanDecisions(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]Decision, error) {
	var out []Decision
	for rows.Next() {
		var d Decision
		var low, used int
		if err := rows.Scan(&d.ID, &d.EmailID, &d.ThreadID, &d.Sender, &d.Subject, &d.Category,
			&d.Confidence, &d.Reason, &d.Summary, &d.Action, &low, &used, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.LowConfidence = intToBool(low)
		d.UsedLLM = intToBool(used)
		out = append(out, d)
	}
	return out, rows.Err()
}
