package store

// Sender allow/deny rule kinds.
const (
	KindAllowImportant = "allow_important"
	KindDenyBulk       = "deny_bulk"
)

// SenderRule is an allow/deny override.
type SenderRule struct {
	Pattern   string // full address or "@domain"
	Kind      string
	Category  string // deny target category (for deny_bulk)
	CreatedAt string
}

// AddSenderRule upserts an allow/deny rule.
func (s *Store) AddSenderRule(pattern, kind, category string) error {
	_, err := s.db.Exec(`
		INSERT INTO sender_rules(pattern, kind, category, created_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(pattern, kind) DO UPDATE SET category = excluded.category`,
		pattern, kind, category, s.nowStr())
	return err
}

// DeleteSenderRule removes a rule.
func (s *Store) DeleteSenderRule(pattern, kind string) error {
	_, err := s.db.Exec("DELETE FROM sender_rules WHERE pattern = ? AND kind = ?", pattern, kind)
	return err
}

// SenderRules returns all allow/deny rules.
func (s *Store) SenderRules() ([]SenderRule, error) {
	rows, err := s.db.Query("SELECT pattern, kind, category, created_at FROM sender_rules ORDER BY pattern")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SenderRule
	for rows.Next() {
		var r SenderRule
		if err := rows.Scan(&r.Pattern, &r.Kind, &r.Category, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SenderOverride implements classify.Lookups: it checks the sender address then
// its domain for an allow/deny rule.
func (s *Store) SenderOverride(sender, domain string) (category string, important bool, matched bool) {
	for _, pat := range []string{sender, "@" + domain} {
		var kind, cat string
		err := s.db.QueryRow(
			"SELECT kind, category FROM sender_rules WHERE pattern = ? ORDER BY kind LIMIT 1", pat).
			Scan(&kind, &cat)
		if err != nil {
			continue
		}
		if kind == KindAllowImportant {
			return "", true, true
		}
		return cat, false, true
	}
	return "", false, false
}

// knownMinObservations is how many consistent observations make a sender's
// category "known" (skipping the LLM).
const knownMinObservations = 5

// KnownCategory implements classify.Lookups. A sender (or its domain) is
// "known" when it has at least knownMinObservations observations and a single
// category accounts for at least 80% of them. Approved Sieve candidates also
// count as known for their domain.
func (s *Store) KnownCategory(sender, domain string) (string, bool) {
	if cat, ok := s.approvedSieveCategory(domain); ok {
		return cat, true
	}
	if cat, ok := s.dominantCategory("sender", sender); ok {
		return cat, true
	}
	if cat, ok := s.dominantCategory("domain", domain); ok {
		return cat, true
	}
	return "", false
}

func (s *Store) approvedSieveCategory(domain string) (string, bool) {
	var cat string
	err := s.db.QueryRow(
		"SELECT category FROM sieve_candidates WHERE domain = ? AND status = 'approved' LIMIT 1", domain).
		Scan(&cat)
	if err != nil {
		return "", false
	}
	return cat, true
}

func (s *Store) dominantCategory(col, val string) (string, bool) {
	if val == "" {
		return "", false
	}
	rows, err := s.db.Query(
		"SELECT category, SUM(count) FROM sender_stats WHERE "+col+" = ? GROUP BY category", val)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	total := 0
	best := ""
	bestN := 0
	for rows.Next() {
		var c string
		var n int
		if err := rows.Scan(&c, &n); err != nil {
			return "", false
		}
		total += n
		if n > bestN {
			bestN, best = n, c
		}
	}
	if total >= knownMinObservations && bestN*100 >= total*80 {
		return best, true
	}
	return "", false
}
