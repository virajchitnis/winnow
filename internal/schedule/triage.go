package schedule

import (
	"context"
	"fmt"

	"winnow/internal/actions"
	"winnow/internal/classify"
	"winnow/internal/config"
	"winnow/internal/jmap"
	"winnow/internal/store"
	"winnow/internal/unsubscribe"
)

const (
	maxChangesPerCycle = 500
	fallbackQueryLimit = 200
)

// triageCycle processes new inbox mail since the last run.
func (s *Scheduler) triageCycle(ctx context.Context) error {
	settings, err := s.store.LoadSettings(s.defaults)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	inbox, ok, err := s.mail.MailboxByRole(ctx, "inbox")
	if err != nil {
		return fmt.Errorf("resolve inbox: %w", err)
	}
	if !ok {
		return fmt.Errorf("no inbox mailbox found")
	}

	state, err := s.store.EmailState()
	if err != nil {
		return err
	}

	// First run: record the current state and process nothing — the existing
	// backlog is handled by the explicit sweep, not by normal triage.
	if state == "" {
		cur, err := s.mail.MailboxState(ctx)
		if err != nil {
			return err
		}
		return s.store.SetEmailState(cur)
	}

	candidateIDs, newState, err := s.changedSince(ctx, state, inbox.ID)
	if err != nil {
		return err
	}
	if len(candidateIDs) == 0 {
		return s.store.SetEmailState(newState)
	}

	emails, err := s.mail.GetEmails(ctx, candidateIDs)
	if err != nil {
		return fmt.Errorf("get emails: %w", err)
	}

	// Keep only mail still in the inbox that we haven't already processed.
	var todo []jmap.Email
	for _, e := range emails {
		if !e.MailboxIDs[inbox.ID] {
			continue
		}
		seen, err := s.store.IsProcessed(e.ID)
		if err != nil {
			return err
		}
		if !seen {
			todo = append(todo, e)
		}
	}
	if len(todo) == 0 {
		return s.store.SetEmailState(newState)
	}

	if err := s.process(ctx, settings, todo, true); err != nil {
		return err
	}
	return s.store.SetEmailState(newState)
}

// changedSince returns candidate email ids and the new state token, falling
// back to a bounded inbox query if the stored state is too old.
func (s *Scheduler) changedSince(ctx context.Context, state, inboxID string) ([]string, string, error) {
	ch, err := s.mail.EmailChanges(ctx, state, maxChangesPerCycle)
	if err == nil {
		ids := append(append([]string{}, ch.Created...), ch.Updated...)
		return ids, ch.NewState, nil
	}
	if !jmap.IsCannotCalculateChanges(err) {
		return nil, "", fmt.Errorf("email changes: %w", err)
	}
	// State too old (e.g. long downtime): fall back to a bounded inbox query.
	s.log.Warn("email state too old; falling back to inbox query")
	ids, err := s.mail.QueryInbox(ctx, inboxID, fallbackQueryLimit)
	if err != nil {
		return nil, "", fmt.Errorf("fallback query: %w", err)
	}
	cur, err := s.mail.MailboxState(ctx)
	if err != nil {
		return nil, "", err
	}
	return ids, cur, nil
}

// process classifies and acts on a batch of emails, recording the outcomes.
// mark controls whether successfully-handled emails are marked processed
// (false for dry-run sweep previews so a later apply re-handles them).
func (s *Scheduler) process(ctx context.Context, settings config.Settings, emails []jmap.Email, mark bool) error {
	cats, err := s.store.Categories()
	if err != nil {
		return err
	}
	catByName := map[string]store.Category{}
	var catInfos []classify.CategoryInfo
	for _, c := range cats {
		catByName[c.Name] = c
		catInfos = append(catInfos, classify.CategoryInfo{Name: c.Name, KeepInInbox: c.KeepInInbox})
	}

	// Spend cap: allow the LLM only if under today's cap.
	used, err := s.store.LLMCallsToday()
	if err != nil {
		return err
	}
	allowLLM := used < settings.LLMDailyCap
	if !allowLLM {
		s.noteSpendCap()
	} else {
		_ = s.store.ResolveErrors("spend_cap")
	}

	mails := make([]classify.Mail, len(emails))
	for i, e := range emails {
		mails[i] = toMail(e)
	}

	results, classifyErr := s.classifier.Classify(ctx, classify.Request{
		Mails:      mails,
		Categories: catInfos,
		Model:      settings.Model,
		Privacy:    settings.Privacy,
		AllowLLM:   allowLLM,
	})
	if classifyErr != nil {
		// Non-fatal: results still hold safe keep-in-inbox fallbacks.
		s.log.Warn("classification degraded", "err", classifyErr)
		_ = s.store.RecordError("classify", classifyErr.Error())
	} else {
		_ = s.store.ResolveErrors("classify")
	}
	if usedLLM(results) {
		_, _ = s.store.AddLLMCalls(1)
	}

	plans := make([]actions.Plan, len(emails))
	lowConf := make([]bool, len(emails))
	for i, e := range emails {
		cat := catByName[results[i].Category]
		p, low := planFor(results[i], cat, settings.ConfidenceThreshold)
		p.EmailID = e.ID
		plans[i] = p
		lowConf[i] = low
	}

	outcomes, applyErr := s.applier.Apply(ctx, plans, settings.DryRun)
	if applyErr != nil {
		s.log.Error("apply failed", "err", applyErr)
		// Fall through to record what we can.
	}

	for i, e := range emails {
		s.record(e, results[i], outcomes, i, lowConf[i], mark)
	}
	return nil
}

// record persists the decision, marks the email processed, and updates the
// learning inputs (sender stats, Sieve candidates, unsubscribe metadata).
func (s *Scheduler) record(e jmap.Email, r classify.Result, outcomes []actions.Result, i int, low, mark bool) {
	action := string(actions.ActionKept)
	if i < len(outcomes) {
		action = string(outcomes[i].Action)
	}
	sender := e.SenderEmail()
	domain := domainOf(sender)

	_ = s.store.RecordDecision(store.Decision{
		EmailID:       e.ID,
		ThreadID:      e.ThreadID,
		Sender:        sender,
		Subject:       e.Subject,
		Category:      r.Category,
		Confidence:    r.Confidence,
		Reason:        r.Reason,
		Summary:       r.Summary,
		Action:        action,
		LowConfidence: low,
		UsedLLM:       r.UsedLLM,
	})

	// A decision is "persisted" only when the action actually stuck — not an
	// error, and not a dry-run preview. The processed mark and every learning
	// side-effect (sender stats, Sieve candidates, unsubscribe metadata) happen
	// only then. This keeps a preview a true side-effect-free dry read: it can be
	// re-run without inflating the rule-learning counters or double-acting, and
	// the decision row written above is its only output.
	persisted := mark && action != string(actions.ActionError) && action != string(actions.ActionDryRun)
	if !persisted {
		return
	}

	_ = s.store.MarkProcessed(e.ID)

	if sender != "" && r.Category != "" {
		_ = s.store.RecordObservation(sender, domain, r.Category)
		if cat, ok, _ := s.store.CategoryByName(r.Category); ok && cat.Moves() && domain != "" {
			_ = s.store.ObserveSieveCandidate(domain, r.Category)
		}
	}

	// Persist unsubscribe metadata so a later unsubscribe works.
	if sender != "" && e.ListUnsubscribe != "" {
		method, target := unsubscribe.Parse(e.ListUnsubscribe, e.ListUnsubscribePost)
		if method != "" {
			_ = s.store.ObserveUnsubscribe(sender, method, target)
		}
	}

	// If this sender was already unsubscribed, bump last_seen so the
	// verification loop knows mail is still arriving from them.
	if sender != "" {
		_ = s.store.TouchUnsubscribeLastSeen(sender)
	}
}

func (s *Scheduler) noteSpendCap() {
	active, _ := s.store.ActiveErrors(50)
	for _, e := range active {
		if e.Kind == "spend_cap" {
			return // already flagged
		}
	}
	_ = s.store.RecordError("spend_cap", "daily LLM-call cap reached; mail left in inbox")
}

func toMail(e jmap.Email) classify.Mail {
	sender := e.SenderEmail()
	return classify.Mail{
		ID:                 e.ID,
		ThreadID:           e.ThreadID,
		Sender:             sender,
		Domain:             domainOf(sender),
		Subject:            e.Subject,
		Preview:            e.Preview,
		HasListUnsubscribe: e.ListUnsubscribe != "",
		HasListID:          e.ListID != "",
		Precedence:         e.Precedence,
	}
}

func usedLLM(results []classify.Result) bool {
	for _, r := range results {
		if r.UsedLLM {
			return true
		}
	}
	return false
}
