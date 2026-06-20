package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"winnow/internal/config"
)

const passwordHashKey = "app_password_hash"

// currentHash returns the active password hash: a DB-stored one (set via the
// dashboard) overrides the env-provided default.
func (s *Server) currentHash() string {
	if v, ok, _ := s.store.GetSetting(passwordHashKey); ok && v != "" {
		return v
	}
	return s.passwordHash
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	st, _ := s.store.LoadSettings(s.defaults)
	s.render(w, r, "settings", "Settings", "settings", map[string]any{
		"Settings": st,
		"Models":   []string{"claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-8"},
	})
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	st, _ := s.store.LoadSettings(s.defaults)

	// Validated, bounded updates.
	if v := r.FormValue("poll_interval"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 5*time.Minute {
			st.PollInterval = d
		}
	}
	if v := r.FormValue("digest_hour"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 23 {
			st.DigestHour = n
		}
	}
	if v := r.FormValue("timezone"); v != "" {
		if _, err := time.LoadLocation(v); err == nil {
			st.Timezone = v
		}
	}
	st.DigestEnabled = r.FormValue("digest_enabled") == "on"
	st.DryRun = r.FormValue("dry_run") == "on"
	if v := r.FormValue("confidence_threshold"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			st.ConfidenceThreshold = f
		}
	}
	if v := r.FormValue("llm_daily_cap"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			st.LLMDailyCap = n
		}
	}
	if v := r.FormValue("model"); v != "" {
		st.Model = v
	}
	if v := r.FormValue("privacy_mode"); v == string(config.PrivacySnippet) || v == string(config.PrivacySubjectSender) {
		st.Privacy = config.PrivacyMode(v)
	}
	if v := r.FormValue("decision_retention_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			st.DecisionRetentionDays = n
		}
	}
	if v := r.FormValue("unsub_verify_window_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			st.UnsubVerifyWindowDays = n
		}
	}

	s.saveSettings(st)
	redirect(w, r, "/settings", "Settings saved.")
}

// saveSettings persists each setting to the DB (the live source of truth).
func (s *Server) saveSettings(st config.Settings) {
	put := func(k, v string) { _ = s.store.SetSetting(k, v) }
	put("dry_run", boolStr(st.DryRun))
	put("timezone", st.Timezone)
	put("digest_hour", strconv.Itoa(st.DigestHour))
	put("digest_enabled", boolStr(st.DigestEnabled))
	put("poll_interval", st.PollInterval.String())
	put("confidence_threshold", strconv.FormatFloat(st.ConfidenceThreshold, 'f', -1, 64))
	put("llm_daily_cap", strconv.Itoa(st.LLMDailyCap))
	put("model", st.Model)
	put("privacy_mode", string(st.Privacy))
	put("decision_retention_days", strconv.Itoa(st.DecisionRetentionDays))
	put("unsub_verify_window_days", strconv.Itoa(st.UnsubVerifyWindowDays))
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	current := r.FormValue("current")
	next := r.FormValue("new")
	if bcrypt.CompareHashAndPassword([]byte(s.currentHash()), []byte(current)) != nil {
		redirect(w, r, "/settings", "Current password is incorrect.")
		return
	}
	if len(next) < 8 {
		redirect(w, r, "/settings", "New password must be at least 8 characters.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), 12)
	if err != nil {
		redirect(w, r, "/settings", "Error: "+err.Error())
		return
	}
	_ = s.store.SetSetting(passwordHashKey, string(hash))
	redirect(w, r, "/settings", "Password changed.")
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	svc := r.FormValue("service")
	var p Pinger
	switch svc {
	case "fastmail":
		p = s.fastmailPing
	case "anthropic":
		p = s.anthropicPing
	default:
		redirect(w, r, "/settings", "Unknown service.")
		return
	}
	if p == nil {
		redirect(w, r, "/settings", svc+" test not available.")
		return
	}
	if err := p.Ping(r.Context()); err != nil {
		redirect(w, r, "/settings", strings.Title(svc)+" connection FAILED: "+err.Error())
		return
	}
	redirect(w, r, "/settings", strings.Title(svc)+" connection OK.")
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
