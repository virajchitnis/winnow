package store

import (
	"database/sql"
	"errors"
)

// --- Idempotency --------------------------------------------------------------

// IsProcessed reports whether the email id has already been acted on.
func (s *Store) IsProcessed(emailID string) (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM processed WHERE email_id = ?", emailID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkProcessed records that an email id has been acted on (idempotent).
func (s *Store) MarkProcessed(emailID string) error {
	_, err := s.db.Exec(
		"INSERT INTO processed(email_id, created_at) VALUES(?, ?) ON CONFLICT(email_id) DO NOTHING",
		emailID, s.nowStr())
	return err
}

// --- Spend cap ----------------------------------------------------------------

// LLMCallsToday returns the number of LLM calls recorded for today (UTC).
func (s *Store) LLMCallsToday() (int, error) {
	day := s.now().UTC().Format("2006-01-02")
	var n int
	err := s.db.QueryRow("SELECT llm_calls FROM spend WHERE day = ?", day).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// AddLLMCalls increments today's LLM-call counter and returns the new total.
func (s *Store) AddLLMCalls(n int) (int, error) {
	day := s.now().UTC().Format("2006-01-02")
	_, err := s.db.Exec(`
		INSERT INTO spend(day, llm_calls) VALUES(?, ?)
		ON CONFLICT(day) DO UPDATE SET llm_calls = llm_calls + excluded.llm_calls`,
		day, n)
	if err != nil {
		return 0, err
	}
	return s.LLMCallsToday()
}

// --- Errors (dashboard banner / digest) --------------------------------------

// AppError is a recorded operational error.
type AppError struct {
	ID        int64
	Kind      string
	Message   string
	CreatedAt string
}

// RecordError stores an error and returns its id.
func (s *Store) RecordError(kind, message string) error {
	// Collapse repeats: if an identical error is already active, just refresh its
	// timestamp instead of inserting a duplicate. This keeps the dashboard banner
	// to one line per distinct error and stops the table growing unbounded when a
	// failure recurs every cycle (e.g. a DNS outage).
	res, err := s.db.Exec(
		"UPDATE errors SET created_at = ? WHERE kind = ? AND message = ? AND resolved = 0",
		s.nowStr(), kind, message)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.db.Exec(
		"INSERT INTO errors(kind, message, created_at, resolved) VALUES(?, ?, ?, 0)",
		kind, message, s.nowStr())
	return err
}

// ActiveErrors returns unresolved errors, newest first.
func (s *Store) ActiveErrors(limit int) ([]AppError, error) {
	rows, err := s.db.Query(
		"SELECT id, kind, message, created_at FROM errors WHERE resolved = 0 ORDER BY id DESC LIMIT ?",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppError
	for rows.Next() {
		var e AppError
		if err := rows.Scan(&e.ID, &e.Kind, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ResolveErrors marks all errors of a kind resolved (e.g. after a healthy poll).
func (s *Store) ResolveErrors(kind string) error {
	_, err := s.db.Exec("UPDATE errors SET resolved = 1 WHERE kind = ? AND resolved = 0", kind)
	return err
}

// --- Sender stats -------------------------------------------------------------

// RecordObservation bumps the sender→category observation count.
func (s *Store) RecordObservation(sender, domain, category string) error {
	_, err := s.db.Exec(`
		INSERT INTO sender_stats(sender, domain, category, count, last_seen)
		VALUES(?, ?, ?, 1, ?)
		ON CONFLICT(sender, category) DO UPDATE SET
			count = count + 1, last_seen = excluded.last_seen`,
		sender, domain, category, s.nowStr())
	return err
}

// DomainCategoryCount returns how many observations a domain has for a category.
func (s *Store) DomainCategoryCount(domain, category string) (int, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COALESCE(SUM(count), 0) FROM sender_stats WHERE domain = ? AND category = ?",
		domain, category).Scan(&n)
	return n, err
}
