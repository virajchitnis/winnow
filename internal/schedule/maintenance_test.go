package schedule

import (
	"context"
	"testing"
	"time"

	"winnow/internal/config"
	"winnow/internal/jmap"
	"winnow/internal/store"
)

func TestRunDailyMaintenancePrunesDecisions(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	defaults.DecisionRetentionDays = 30

	// Record a decision timestamped 60 days ago using a back-dated store clock.
	old := time.Now().UTC().AddDate(0, 0, -60)
	stOld, err := store.Open(t.TempDir()+"/old.db", store.WithClock(func() time.Time { return old }))
	if err != nil {
		t.Fatal(err)
	}
	// Seed into the real store via the shared DB path — instead, just use the
	// main store with a clock override for the insert then restore.
	stOld.Close()

	// Simpler: insert directly via the store's DB.
	_, err = st.DB().Exec(
		`INSERT INTO decisions(email_id,sender,subject,category,confidence,action,low_confidence,used_llm,created_at)
		 VALUES('old-e1','a@b.com','hi','Promotional',0.9,'moved',0,0,?)`,
		old.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB().Exec(
		`INSERT INTO decisions(email_id,sender,subject,category,confidence,action,low_confidence,used_llm,created_at)
		 VALUES('new-e1','a@b.com','hi','Promotional',0.9,'moved',0,0,?)`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	s := New(Deps{Store: st, Mail: fj, Defaults: defaults})
	s.runDailyMaintenance(config.Settings{DecisionRetentionDays: 30})

	decisions, _ := st.RecentDecisions(10)
	for _, d := range decisions {
		if d.EmailID == "old-e1" {
			t.Error("old decision should have been pruned")
		}
	}
	found := false
	for _, d := range decisions {
		if d.EmailID == "new-e1" {
			found = true
		}
	}
	if !found {
		t.Error("recent decision should be retained")
	}
}

func TestRunDailyMaintenanceVerifiesUnsubscribes(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	defaults.UnsubVerifyWindowDays = 14

	// Insert an unsubscribed sender whose window has elapsed and no mail since.
	actedAt := time.Now().UTC().AddDate(0, 0, -20)
	_, err := st.DB().Exec(`
		INSERT INTO unsubscribe(sender,method,target,status,count,last_seen,acted_at,verified)
		VALUES('done@clean.com','one_click','https://x.com','unsubscribed',2,?,?,0)`,
		actedAt.Format(time.RFC3339Nano),
		actedAt.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	s := New(Deps{Store: st, Mail: fj, Defaults: defaults})
	s.runDailyMaintenance(config.Settings{UnsubVerifyWindowDays: 14})

	rec, ok, _ := st.UnsubscribeRecordBySender("done@clean.com")
	if !ok || !rec.Verified {
		t.Error("done@clean.com should be marked verified after window elapsed")
	}
}

func TestTouchUnsubLastSeenDuringTriage(t *testing.T) {
	st, fj, defaults := sweepSetup(t)

	// Pre-insert an unsubscribed sender.
	actedAt := time.Now().UTC().AddDate(0, 0, -20)
	_, _ = st.DB().Exec(`
		INSERT INTO unsubscribe(sender,method,target,status,count,last_seen,acted_at,verified)
		VALUES('pesky@spam.com','one_click','https://x.com','unsubscribed',1,?,?,0)`,
		actedAt.Format(time.RFC3339Nano),
		actedAt.Format(time.RFC3339Nano))

	// Triage delivers a new mail from that sender.
	_ = st.SetEmailState("s0")
	fj.changes = &jmap.Changes{NewState: "s1", Created: []string{"e99"}}
	fj.emails = map[string]jmap.Email{
		"e99": {
			ID:         "e99",
			MailboxIDs: map[string]bool{"mb-inbox": true},
			From:       []jmap.EmailAddress{{Email: "pesky@spam.com"}},
			Subject:    "Still here!",
		},
	}
	fj.inbox = jmap.Mailbox{ID: "mb-inbox", Role: "inbox"}

	s := newSched(t, st, fj, defaults, `[{"i":0,"category":"Promotional","confidence":0.95}]`)
	s.TriageOnce(context.Background())

	// last_seen should now be after acted_at, so MarkVerifiedUnsubscribes won't verify.
	n, _ := st.MarkVerifiedUnsubscribes(14)
	if n != 0 {
		t.Errorf("pesky@spam.com still sending; should not be verified, got %d", n)
	}
}
