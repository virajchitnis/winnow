package store

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"winnow/internal/config"
)

// Setting keys persisted in the settings table.
const (
	keyDryRun          = "dry_run"
	keyTimezone        = "timezone"
	keyDigestHour      = "digest_hour"
	keyDigestEnabled   = "digest_enabled"
	keyPollInterval    = "poll_interval"
	keyConfidence      = "confidence_threshold"
	keyLLMDailyCap     = "llm_daily_cap"
	keyModel           = "model"
	keyPrivacy         = "privacy_mode"
	keyRetentionDays   = "decision_retention_days"
	keyUnsubVerifyDays = "unsub_verify_window_days"
	keyNewsletterSum   = "newsletter_summaries"
	keyEmailState      = "email_state"     // JMAP Email/changes state token
	keyHighWaterRecv   = "high_water_recv" // newest receivedAt processed
	keyLastDigestAt    = "last_digest_at"  // send time of the latest digest/briefing
)

// GetSetting returns a raw setting value and whether it exists.
func (s *Store) GetSetting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting upserts a raw setting value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// SeedSettings inserts default values for any settings key not already present.
// Called once on first boot with the env-derived defaults.
func (s *Store) SeedSettings(d config.Settings) error {
	seed := map[string]string{
		keyDryRun:          boolStr(d.DryRun),
		keyTimezone:        d.Timezone,
		keyDigestHour:      strconv.Itoa(d.DigestHour),
		keyDigestEnabled:   boolStr(d.DigestEnabled),
		keyPollInterval:    d.PollInterval.String(),
		keyConfidence:      strconv.FormatFloat(d.ConfidenceThreshold, 'f', -1, 64),
		keyLLMDailyCap:     strconv.Itoa(d.LLMDailyCap),
		keyModel:           d.Model,
		keyPrivacy:         string(d.Privacy),
		keyRetentionDays:   strconv.Itoa(d.DecisionRetentionDays),
		keyUnsubVerifyDays: strconv.Itoa(d.UnsubVerifyWindowDays),
		keyNewsletterSum:   boolStr(d.NewsletterSummaries),
	}
	for k, v := range seed {
		if _, ok, err := s.GetSetting(k); err != nil {
			return err
		} else if ok {
			continue
		}
		if err := s.SetSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

// LoadSettings reads the live settings, falling back to the given defaults for
// any missing/unparseable key.
func (s *Store) LoadSettings(def config.Settings) (config.Settings, error) {
	out := def
	rows, err := s.db.Query("SELECT key, value FROM settings")
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		applySetting(&out, k, v)
	}
	return out, rows.Err()
}

func applySetting(s *config.Settings, k, v string) {
	switch k {
	case keyDryRun:
		s.DryRun = parseBool(v, s.DryRun)
	case keyTimezone:
		if v != "" {
			s.Timezone = v
		}
	case keyDigestHour:
		if n, err := strconv.Atoi(v); err == nil {
			s.DigestHour = n
		}
	case keyDigestEnabled:
		s.DigestEnabled = parseBool(v, s.DigestEnabled)
	case keyPollInterval:
		if d, err := time.ParseDuration(v); err == nil {
			s.PollInterval = d
		}
	case keyConfidence:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			s.ConfidenceThreshold = f
		}
	case keyLLMDailyCap:
		if n, err := strconv.Atoi(v); err == nil {
			s.LLMDailyCap = n
		}
	case keyModel:
		if v != "" {
			s.Model = v
		}
	case keyPrivacy:
		if v != "" {
			s.Privacy = config.PrivacyMode(v)
		}
	case keyRetentionDays:
		if n, err := strconv.Atoi(v); err == nil {
			s.DecisionRetentionDays = n
		}
	case keyUnsubVerifyDays:
		if n, err := strconv.Atoi(v); err == nil {
			s.UnsubVerifyWindowDays = n
		}
	case keyNewsletterSum:
		s.NewsletterSummaries = parseBool(v, s.NewsletterSummaries)
	}
}

// EmailState / HighWaterReceived helpers for the scheduler.

// EmailState returns the stored JMAP Email/changes state token (or "").
func (s *Store) EmailState() (string, error) {
	v, _, err := s.GetSetting(keyEmailState)
	return v, err
}

// SetEmailState stores the JMAP Email/changes state token.
func (s *Store) SetEmailState(token string) error { return s.SetSetting(keyEmailState, token) }

// LastDigestAt returns when the last digest/briefing was sent (RFC3339), or ""
// if one has never been sent.
func (s *Store) LastDigestAt() (string, error) {
	v, _, err := s.GetSetting(keyLastDigestAt)
	return v, err
}

// SetLastDigestAt records the send time of the latest digest/briefing.
func (s *Store) SetLastDigestAt(ts string) error { return s.SetSetting(keyLastDigestAt, ts) }

// NewsletterConfig returns whether the opt-in newsletter summaries are enabled
// and the model to use for them.
func (s *Store) NewsletterConfig() (bool, string, error) {
	on := false
	if v, ok, err := s.GetSetting(keyNewsletterSum); err != nil {
		return false, "", err
	} else if ok {
		on = parseBool(v, false)
	}
	model := "claude-haiku-4-5"
	if v, ok, _ := s.GetSetting(keyModel); ok && v != "" {
		model = v
	}
	return on, model, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// parseBool accepts the canonical strconv forms plus the HTML checkbox forms
// ("on"/"off"), so values written by either the seed or the dashboard parse
// correctly. Unknown values fall back to def.
func parseBool(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "t", "1", "on", "yes":
		return true
	case "false", "f", "0", "off", "no":
		return false
	default:
		return def
	}
}
