package store

import "time"

// Unsubscribe statuses.
const (
	UnsubNeedsDecision = "needs_decision"
	UnsubKept          = "kept"
	UnsubUnsubscribed  = "unsubscribed"
)

// Unsubscribe methods.
const (
	UnsubMethodOneClick = "one_click"
	UnsubMethodMailto   = "mailto"
	UnsubMethodHTTP     = "http_manual"
)

// UnsubscribeRecord is the per-sender unsubscribe state.
type UnsubscribeRecord struct {
	Sender   string
	Method   string
	Target   string
	Status   string
	Count    int
	LastSeen string
	ActedAt  string
	Verified bool
}

// ObserveUnsubscribe records that an unsubscribe-capable email was seen from a
// sender, persisting the method/target so a later unsubscribe works without a
// fresh message. It bumps the sighting count and leaves an existing decision
// (kept/unsubscribed) untouched.
func (s *Store) ObserveUnsubscribe(sender, method, target string) error {
	_, err := s.db.Exec(`
		INSERT INTO unsubscribe(sender, method, target, status, count, last_seen)
		VALUES(?, ?, ?, 'needs_decision', 1, ?)
		ON CONFLICT(sender) DO UPDATE SET
			method = excluded.method,
			target = excluded.target,
			count = count + 1,
			last_seen = excluded.last_seen`,
		sender, method, target, s.nowStr())
	return err
}

// UnsubscribeCandidates returns records filtered by status (empty = all),
// ranked by sighting count.
func (s *Store) UnsubscribeCandidates(status string) ([]UnsubscribeRecord, error) {
	q := "SELECT sender, method, target, status, count, last_seen, COALESCE(acted_at, ''), verified FROM unsubscribe"
	args := []any{}
	if status != "" {
		q += " WHERE status = ?"
		args = append(args, status)
	}
	q += " ORDER BY count DESC, sender"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnsubscribeRecord
	for rows.Next() {
		var r UnsubscribeRecord
		var verified int
		if err := rows.Scan(&r.Sender, &r.Method, &r.Target, &r.Status, &r.Count, &r.LastSeen, &r.ActedAt, &verified); err != nil {
			return nil, err
		}
		r.Verified = intToBool(verified)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UnsubscribeRecordBySender returns one record, or ok=false.
func (s *Store) UnsubscribeRecordBySender(sender string) (UnsubscribeRecord, bool, error) {
	row := s.db.QueryRow(
		"SELECT sender, method, target, status, count, last_seen, COALESCE(acted_at, ''), verified FROM unsubscribe WHERE sender = ?",
		sender)
	var r UnsubscribeRecord
	var verified int
	err := row.Scan(&r.Sender, &r.Method, &r.Target, &r.Status, &r.Count, &r.LastSeen, &r.ActedAt, &verified)
	if err != nil {
		return UnsubscribeRecord{}, false, nil //nolint:nilerr
	}
	r.Verified = intToBool(verified)
	return r, true, nil
}

// SetUnsubscribeStatus records a decision (kept/unsubscribed) for a sender.
func (s *Store) SetUnsubscribeStatus(sender, status string, acted bool) error {
	if acted {
		_, err := s.db.Exec(
			"UPDATE unsubscribe SET status = ?, acted_at = ? WHERE sender = ?",
			status, s.nowStr(), sender)
		return err
	}
	_, err := s.db.Exec("UPDATE unsubscribe SET status = ? WHERE sender = ?", status, sender)
	return err
}

// SetUnsubscribeVerified marks whether a sender kept emailing after an
// unsubscribe (false once mail is still arriving, prompting the rule fallback).
func (s *Store) SetUnsubscribeVerified(sender string, verified bool) error {
	_, err := s.db.Exec("UPDATE unsubscribe SET verified = ? WHERE sender = ?", boolToInt(verified), sender)
	return err
}

// TouchUnsubscribeLastSeen bumps last_seen for a sender that is already in the
// 'unsubscribed' state. Called during triage so the verification loop knows
// mail is still arriving even if the new message lacks a List-Unsubscribe header.
func (s *Store) TouchUnsubscribeLastSeen(sender string) error {
	_, err := s.db.Exec(
		"UPDATE unsubscribe SET last_seen = ? WHERE sender = ? AND status = 'unsubscribed'",
		s.nowStr(), sender)
	return err
}

// MarkVerifiedUnsubscribes finds unsubscribed-but-unverified senders whose
// verification window has elapsed and marks those with no mail since acted_at
// as verified. Returns the count of newly-verified senders.
func (s *Store) MarkVerifiedUnsubscribes(windowDays int) (int, error) {
	cutoff := s.now().UTC().AddDate(0, 0, -windowDays).Format(time.RFC3339Nano)
	res, err := s.db.Exec(`
		UPDATE unsubscribe SET verified = 1
		WHERE status = 'unsubscribed' AND verified = 0
		  AND acted_at != '' AND acted_at <= ?
		  AND last_seen <= acted_at`,
		cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
