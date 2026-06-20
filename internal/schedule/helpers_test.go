package schedule

import (
	"context"
	"testing"
	"time"

	"winnow/internal/config"
	"winnow/internal/jmap"
)

func TestClampInterval(t *testing.T) {
	if got := clampInterval(time.Minute); got != 5*time.Minute {
		t.Errorf("clamp below min = %s", got)
	}
	if got := clampInterval(30 * time.Minute); got != 30*time.Minute {
		t.Errorf("clamp above min = %s", got)
	}
}

func TestUntilNextDigest(t *testing.T) {
	fixed := time.Date(2026, 6, 18, 5, 0, 0, 0, time.UTC)
	s := &Scheduler{now: func() time.Time { return fixed }}
	// Digest at 07:00 UTC -> 2h away.
	d := s.untilNextDigest(config.Settings{Timezone: "UTC", DigestHour: 7})
	if d != 2*time.Hour {
		t.Errorf("until next digest = %s, want 2h", d)
	}
	// Digest hour already passed today -> tomorrow.
	d = s.untilNextDigest(config.Settings{Timezone: "UTC", DigestHour: 3})
	if d <= 0 || d > 24*time.Hour {
		t.Errorf("past-hour digest = %s", d)
	}
	// Bad timezone falls back to UTC (no panic).
	_ = s.untilNextDigest(config.Settings{Timezone: "Nowhere/Nope", DigestHour: 7})
}

type recordingDigester struct{ sent bool }

func (r *recordingDigester) Send(context.Context) error { r.sent = true; return nil }

func TestMaybeSendDigest(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	defaults.DigestEnabled = true
	_ = st.SetSetting("digest_enabled", "true")
	dg := &recordingDigester{}
	s := New(Deps{Store: st, Mail: fj, Defaults: defaults, Digester: dg})
	s.maybeSendDigest(context.Background())
	if !dg.sent {
		t.Error("digest should have been sent when enabled")
	}

	// Disabled -> not sent.
	_ = st.SetSetting("digest_enabled", "false")
	dg2 := &recordingDigester{}
	s2 := New(Deps{Store: st, Mail: fj, Defaults: defaults, Digester: dg2})
	s2.maybeSendDigest(context.Background())
	if dg2.sent {
		t.Error("digest should not be sent when disabled")
	}
}

func TestSpendCapLeavesMailInInbox(t *testing.T) {
	st, fj, defaults := sweepSetup(t)
	defaults.LLMDailyCap = 0 // cap reached immediately
	_ = st.SetSetting("llm_daily_cap", "0")
	_ = st.SetEmailState("s1")
	fj.changes = &jmap.Changes{NewState: "s2", Created: []string{"e1"}}
	s := newSched(t, st, fj, defaults, `[{"i":0,"category":"Promotional","confidence":0.95}]`)

	s.TriageOnce(context.Background())

	if len(fj.updated) != 0 {
		t.Error("spend cap reached: mail should be left in inbox (no move)")
	}
	active, _ := st.ActiveErrors(10)
	found := false
	for _, e := range active {
		if e.Kind == "spend_cap" {
			found = true
		}
	}
	if !found {
		t.Error("spend cap should be recorded as an error")
	}
}
