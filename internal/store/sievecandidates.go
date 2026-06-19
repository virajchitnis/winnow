package store

// Sieve candidate statuses.
const (
	SieveProposed = "proposed"
	SieveApproved = "approved"
	SieveRejected = "rejected"
)

// SieveCandidate is a domain→category rule candidate.
type SieveCandidate struct {
	Domain       string
	Category     string
	Observations int
	Status       string
	UpdatedAt    string
}

// ObserveSieveCandidate bumps the observation count for a domain→category pair,
// creating it in "proposed" status if new. Rejected candidates are left as-is.
func (s *Store) ObserveSieveCandidate(domain, category string) error {
	_, err := s.db.Exec(`
		INSERT INTO sieve_candidates(domain, category, observations, status, updated_at)
		VALUES(?, ?, 1, 'proposed', ?)
		ON CONFLICT(domain, category) DO UPDATE SET
			observations = observations + 1,
			updated_at = excluded.updated_at`,
		domain, category, s.nowStr())
	return err
}

// SieveCandidates returns candidates filtered by status (empty = all).
func (s *Store) SieveCandidates(status string) ([]SieveCandidate, error) {
	q := "SELECT domain, category, observations, status, updated_at FROM sieve_candidates"
	args := []any{}
	if status != "" {
		q += " WHERE status = ?"
		args = append(args, status)
	}
	q += " ORDER BY observations DESC, domain"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SieveCandidate
	for rows.Next() {
		var c SieveCandidate
		if err := rows.Scan(&c.Domain, &c.Category, &c.Observations, &c.Status, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetSieveCandidateStatus updates a candidate's status (approve/reject).
func (s *Store) SetSieveCandidateStatus(domain, category, status string) error {
	_, err := s.db.Exec(
		"UPDATE sieve_candidates SET status = ?, updated_at = ? WHERE domain = ? AND category = ?",
		status, s.nowStr(), domain, category)
	return err
}

// --- Sieve script backups ----------------------------------------------------

// BackupSieve stores a copy of the active script before a managed-block write.
func (s *Store) BackupSieve(content string) error {
	_, err := s.db.Exec("INSERT INTO sieve_backups(content, created_at) VALUES(?, ?)", content, s.nowStr())
	return err
}

// LatestSieveBackup returns the most recent backup content, or ok=false.
func (s *Store) LatestSieveBackup() (string, bool, error) {
	var content string
	err := s.db.QueryRow("SELECT content FROM sieve_backups ORDER BY id DESC LIMIT 1").Scan(&content)
	if err != nil {
		return "", false, nil //nolint:nilerr // no backup yet is not an error
	}
	return content, true, nil
}
