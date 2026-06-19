// Package store is Winnow's SQLite persistence layer. It owns the schema
// (versioned, embedded migrations applied on Open), the live settings, the
// decision log, sender stats, Sieve state, unsubscribe state, errors, and the
// daily spend counter.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the database connection.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock overrides the time source (used in tests).
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// Open opens (or creates) the SQLite database at path, applies migrations, and
// returns a ready Store.
func Open(path string, opts ...Option) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite is single-writer; pin to one connection to avoid "database is
	// locked" and to keep per-connection pragmas in effect.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	s := &Store{db: db, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying *sql.DB (for advanced/maintenance use).
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) nowStr() string { return s.now().UTC().Format(time.RFC3339Nano) }

// migrate applies any embedded migrations newer than PRAGMA user_version.
func (s *Store) migrate() error {
	var current int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	type mig struct {
		version int
		name    string
	}
	var migs []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := versionFromName(e.Name())
		if err != nil {
			return fmt.Errorf("migration %q: %w", e.Name(), err)
		}
		migs = append(migs, mig{version: v, name: e.Name()})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })

	for _, m := range migs {
		if m.version <= current {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %q: %w", m.name, err)
		}
		// PRAGMA can't be parameterized; version comes from a trusted filename.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func versionFromName(name string) (int, error) {
	// Expect "NNNN_description.sql".
	us := strings.IndexByte(name, '_')
	if us <= 0 {
		return 0, fmt.Errorf("missing version prefix")
	}
	var v int
	if _, err := fmt.Sscanf(name[:us], "%d", &v); err != nil {
		return 0, err
	}
	if v <= 0 {
		return 0, fmt.Errorf("version must be positive")
	}
	return v, nil
}
