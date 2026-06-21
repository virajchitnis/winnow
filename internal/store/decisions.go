package store

import "fmt"

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

// DecisionStats summarizes the whole decision log for the Review header.
type DecisionStats struct {
	Total         int
	LowConfidence int
	UsedLLM       int
}

// DecisionStats returns all-time totals across the decision log.
func (s *Store) DecisionStats() (DecisionStats, error) {
	var d DecisionStats
	err := s.db.QueryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(low_confidence), 0),
			COALESCE(SUM(used_llm), 0)
		FROM decisions`).Scan(&d.Total, &d.LowConfidence, &d.UsedLLM)
	return d, err
}

// DecisionQuery filters, orders, and pages the decision log for the Review tab.
type DecisionQuery struct {
	Search string // substring match on sender, subject, or category (case-insensitive)
	Sort   string // date | confidence | category | sender (default date)
	Desc   bool
	Limit  int
	Offset int
}

// SortableDecisionColumns whitelists sortable columns so the user-supplied sort
// key can never be injected into the SQL.
var SortableDecisionColumns = map[string]string{
	"date":       "created_at",
	"confidence": "confidence",
	"category":   "category",
	"sender":     "sender",
}

// QueryDecisions returns decisions matching the query, ordered and paged. Both
// the sort column (whitelisted) and direction are validated before use; the
// search term is always parameterized.
func (s *Store) QueryDecisions(q DecisionQuery) ([]Decision, error) {
	col, ok := SortableDecisionColumns[q.Sort]
	if !ok {
		col = "created_at"
	}
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	var where string
	args := []any{}
	if q.Search != "" {
		where = "WHERE sender LIKE ? OR subject LIKE ? OR category LIKE ?"
		like := "%" + q.Search + "%"
		args = append(args, like, like, like)
	}
	query := fmt.Sprintf(`
		SELECT id, email_id, thread_id, sender, subject, category, confidence,
			reason, summary, action, low_confidence, used_llm, created_at
		FROM decisions %s
		ORDER BY %s %s, id DESC
		LIMIT ? OFFSET ?`, where, col, dir)
	args = append(args, q.Limit, q.Offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDecisions(rows)
}

// PendingPreviewDecisions returns the preview (dry_run) decisions — the ones
// previewed but never applied. RecordDecision keeps at most one dry_run row per
// email, so each row here is the latest preview for a distinct email.
func (s *Store) PendingPreviewDecisions() ([]Decision, error) {
	rows, err := s.db.Query(`
		SELECT id, email_id, thread_id, sender, subject, category, confidence,
			reason, summary, action, low_confidence, used_llm, created_at
		FROM decisions WHERE action = 'dry_run' ORDER BY id DESC`)
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
