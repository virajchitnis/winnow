package store

import (
	"testing"
	"time"
)

func TestMarkVerifiedUnsubscribes(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	s := testStoreWithClock(t, func() time.Time { return now })

	// Unsubscribed 20 days ago, no mail since.
	actedAt := now.AddDate(0, 0, -20)
	s2 := testStoreWithClock(t, func() time.Time { return actedAt })
	_ = s2.ObserveUnsubscribe("old@gone.com", UnsubMethodOneClick, "https://gone.com/u")
	_ = s2.SetUnsubscribeStatus("old@gone.com", UnsubUnsubscribed, true)
	// Copy the row into our main store via direct SQL (same DB file is not shared,
	// so we work through the real store clock by seeding via s2 then closing it).
	// Simpler: just manipulate through s, back-dating acted_at directly.
	s2.Close()

	// Use the main store whose clock is `now`. Insert the record manually.
	_, err := s.db.Exec(`
		INSERT INTO unsubscribe(sender, method, target, status, count, last_seen, acted_at, verified)
		VALUES('success@clean.com', 'one_click', 'https://clean.com/u',
		       'unsubscribed', 3, ?, ?, 0)`,
		actedAt.UTC().Format("2006-01-02T15:04:05Z"),
		actedAt.UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		t.Fatal(err)
	}

	// Still sending: acted_at 20 days ago but last_seen is after acted_at.
	lastSeenRecent := now.AddDate(0, 0, -2)
	_, err = s.db.Exec(`
		INSERT INTO unsubscribe(sender, method, target, status, count, last_seen, acted_at, verified)
		VALUES('still@sending.com', 'one_click', 'https://sending.com/u',
		       'unsubscribed', 5, ?, ?, 0)`,
		lastSeenRecent.UTC().Format("2006-01-02T15:04:05Z"),
		actedAt.UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		t.Fatal(err)
	}

	// Window not elapsed yet: acted_at only 5 days ago.
	recentAct := now.AddDate(0, 0, -5)
	_, err = s.db.Exec(`
		INSERT INTO unsubscribe(sender, method, target, status, count, last_seen, acted_at, verified)
		VALUES('too@fresh.com', 'one_click', 'https://fresh.com/u',
		       'unsubscribed', 1, ?, ?, 0)`,
		recentAct.UTC().Format("2006-01-02T15:04:05Z"),
		recentAct.UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.MarkVerifiedUnsubscribes(14)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("want 1 verified, got %d", n)
	}

	rec, ok, _ := s.UnsubscribeRecordBySender("success@clean.com")
	if !ok || !rec.Verified {
		t.Error("success@clean.com should be verified")
	}
	rec, ok, _ = s.UnsubscribeRecordBySender("still@sending.com")
	if !ok || rec.Verified {
		t.Error("still@sending.com should NOT be verified (still sending)")
	}
	rec, ok, _ = s.UnsubscribeRecordBySender("too@fresh.com")
	if !ok || rec.Verified {
		t.Error("too@fresh.com should NOT be verified (window not elapsed)")
	}
}

func TestTouchUnsubscribeLastSeen(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	s := testStoreWithClock(t, func() time.Time { return now })

	actedAt := now.AddDate(0, 0, -20)
	_, _ = s.db.Exec(`
		INSERT INTO unsubscribe(sender, method, target, status, count, last_seen, acted_at, verified)
		VALUES('bump@me.com','one_click','https://x.com','unsubscribed',1,?,?,0)`,
		actedAt.UTC().Format("2006-01-02T15:04:05Z"),
		actedAt.UTC().Format("2006-01-02T15:04:05Z"))

	if err := s.TouchUnsubscribeLastSeen("bump@me.com"); err != nil {
		t.Fatal(err)
	}

	rec, _, _ := s.UnsubscribeRecordBySender("bump@me.com")
	// After touch, last_seen > acted_at, so MarkVerifiedUnsubscribes should NOT mark it verified.
	n, _ := s.MarkVerifiedUnsubscribes(14)
	if n != 0 {
		t.Errorf("should not be verified after touch: got %d verified", n)
	}
	_ = rec

	// Touch is a no-op for senders not in 'unsubscribed' state.
	_ = s.ObserveUnsubscribe("pending@x.com", UnsubMethodHTTP, "https://x.com/u")
	if err := s.TouchUnsubscribeLastSeen("pending@x.com"); err != nil {
		t.Fatal(err)
	}
	// Still needs_decision, no change expected.
	pr, _, _ := s.UnsubscribeRecordBySender("pending@x.com")
	if pr.Status != UnsubNeedsDecision {
		t.Errorf("status should be unchanged: %q", pr.Status)
	}
}

func testStoreWithClock(t *testing.T, clk func() time.Time) *Store {
	t.Helper()
	path := t.TempDir() + "/test.db"
	s, err := Open(path, WithClock(clk))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
