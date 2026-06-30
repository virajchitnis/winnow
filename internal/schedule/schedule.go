// Package schedule wires the triage pipeline together and runs it on an
// interval, plus the one-time initial sweep and the daily digest trigger. It
// enforces a single-flight run lock, the LLM spend cap, and graceful shutdown.
package schedule

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/config"
	"winnow/internal/jmap"
	"winnow/internal/store"
)

// Mailer is the JMAP surface the scheduler needs (mockable in tests).
type Mailer interface {
	MailboxByRole(ctx context.Context, role string) (jmap.Mailbox, bool, error)
	MailboxState(ctx context.Context) (string, error)
	EmailChanges(ctx context.Context, since string, max int) (*jmap.Changes, error)
	QueryInbox(ctx context.Context, mailboxID string, limit int) ([]string, error)
	GetEmails(ctx context.Context, ids []string) ([]jmap.Email, error)
}

// Classifier classifies a batch of mail.
type Classifier interface {
	Classify(ctx context.Context, req classify.Request) ([]classify.Result, error)
}

// Applier applies action plans.
type Applier interface {
	Apply(ctx context.Context, plans []actions.Plan, dryRun bool) ([]actions.Result, error)
}

// Digester sends the daily digest.
type Digester interface {
	Send(ctx context.Context) error
}

// Health is a snapshot of scheduler liveness for /healthz.
type Health struct {
	LastPollAt    time.Time
	LastPollOK    bool
	LastPollError string
	Running       bool
}

// Scheduler runs triage, sweeps, and the digest trigger.
type Scheduler struct {
	store      *store.Store
	mail       Mailer
	classifier Classifier
	applier    Applier
	digester   Digester
	defaults   config.Settings
	log        *slog.Logger
	now        func() time.Time

	runLock chan struct{} // capacity 1; single-flight guard

	mu     sync.Mutex
	health Health
}

// Deps bundles the scheduler's dependencies.
type Deps struct {
	Store      *store.Store
	Mail       Mailer
	Classifier Classifier
	Applier    Applier
	Digester   Digester
	Defaults   config.Settings
	Logger     *slog.Logger
	Now        func() time.Time
}

// New constructs a Scheduler.
func New(d Deps) *Scheduler {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		store:      d.Store,
		mail:       d.Mail,
		classifier: d.Classifier,
		applier:    d.Applier,
		digester:   d.Digester,
		defaults:   d.Defaults,
		log:        log,
		now:        now,
		runLock:    make(chan struct{}, 1),
	}
}

// HealthSnapshot returns the current health for /healthz.
func (s *Scheduler) HealthSnapshot() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health
}

// Run starts the poll loop and digest trigger and blocks until ctx is
// cancelled. The current cycle is allowed to finish on cancellation.
func (s *Scheduler) Run(ctx context.Context) {
	settings, _ := s.store.LoadSettings(s.defaults)

	pollTicker := time.NewTicker(clampInterval(settings.PollInterval))
	defer pollTicker.Stop()

	digestTimer := time.NewTimer(s.untilNextDigest(settings))
	defer digestTimer.Stop()

	// Run an initial triage shortly after start.
	go s.TriageOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopping")
			return
		case <-pollTicker.C:
			s.TriageOnce(ctx)
			// Re-read the (possibly edited) interval and reset the ticker.
			if cur, err := s.store.LoadSettings(s.defaults); err == nil {
				pollTicker.Reset(clampInterval(cur.PollInterval))
			}
		case <-digestTimer.C:
			s.maybeSendDigest(ctx)
			cur, _ := s.store.LoadSettings(s.defaults)
			digestTimer.Reset(s.untilNextDigest(cur))
		}
	}
}

// TriageOnce runs a single triage cycle if no run is already in progress.
func (s *Scheduler) TriageOnce(ctx context.Context) {
	select {
	case s.runLock <- struct{}{}:
		defer func() { <-s.runLock }()
	default:
		s.log.Debug("triage skipped: a run is already in progress")
		return
	}

	s.setRunning(true)
	err := s.triageCycle(ctx)
	s.setRunning(false)
	s.recordPoll(err)
	if err != nil {
		s.log.Error("triage cycle failed", "err", err)
		_ = s.store.RecordError("triage", err.Error())
	} else {
		_ = s.store.ResolveErrors("triage")
	}
}

// SendDigestNow sends the morning briefing immediately (the dashboard's
// "Send briefing now" button). An explicit user action — it ignores the
// DigestEnabled toggle but otherwise behaves like the scheduled send.
func (s *Scheduler) SendDigestNow(ctx context.Context) error {
	if s.digester == nil {
		return fmt.Errorf("digest not configured")
	}
	return s.digester.Send(ctx)
}

func (s *Scheduler) maybeSendDigest(ctx context.Context) {
	settings, _ := s.store.LoadSettings(s.defaults)
	if settings.DigestEnabled && s.digester != nil {
		if err := s.digester.Send(ctx); err != nil {
			s.log.Error("digest failed", "err", err)
			_ = s.store.RecordError("digest", err.Error())
		}
	}
	s.runDailyMaintenance(settings)
}

// runDailyMaintenance performs housekeeping tasks that run once a day alongside
// the digest: pruning old decisions and verifying completed unsubscribes.
func (s *Scheduler) runDailyMaintenance(settings config.Settings) {
	if settings.DecisionRetentionDays > 0 {
		cutoff := s.now().UTC().AddDate(0, 0, -settings.DecisionRetentionDays).Format(time.RFC3339Nano)
		if n, err := s.store.PruneDecisions(cutoff); err != nil {
			s.log.Error("prune decisions", "err", err)
		} else if n > 0 {
			s.log.Info("pruned old decisions", "count", n)
		}
	}
	if settings.UnsubVerifyWindowDays > 0 {
		if n, err := s.store.MarkVerifiedUnsubscribes(settings.UnsubVerifyWindowDays); err != nil {
			s.log.Error("verify unsubscribes", "err", err)
		} else if n > 0 {
			s.log.Info("marked unsubscribes verified", "count", n)
		}
	}
}

func (s *Scheduler) setRunning(r bool) {
	s.mu.Lock()
	s.health.Running = r
	s.mu.Unlock()
}

func (s *Scheduler) recordPoll(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health.LastPollAt = s.now()
	s.health.LastPollOK = err == nil
	if err != nil {
		s.health.LastPollError = err.Error()
	} else {
		s.health.LastPollError = ""
	}
}

// untilNextDigest returns the duration until the next digest fire time.
func (s *Scheduler) untilNextDigest(st config.Settings) time.Duration {
	loc, err := time.LoadLocation(st.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := s.now().In(loc)
	next := time.Date(now.Year(), now.Month(), now.Day(), st.DigestHour, 0, 0, 0, loc)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

func clampInterval(d time.Duration) time.Duration {
	const minInterval = 5 * time.Minute
	if d < minInterval {
		return minInterval
	}
	return d
}
