// Package config loads Winnow's bootstrap configuration from the environment.
//
// Only secrets and bootstrap values live here. Operational settings (poll
// interval, confidence threshold, model, privacy mode, spend cap, …) are stored
// in the database and edited from the dashboard; the values loaded here merely
// SEED those settings on first boot. See internal/store for the live settings.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// PrivacyMode controls how much of each email is sent to the LLM.
type PrivacyMode string

const (
	// PrivacySnippet sends subject + sender + a short body snippet (default).
	PrivacySnippet PrivacyMode = "snippet"
	// PrivacySubjectSender sends only subject + sender — no body content.
	PrivacySubjectSender PrivacyMode = "subject_sender"
)

// Config holds bootstrap configuration read from the environment.
type Config struct {
	// Secrets.
	FastmailToken   string
	AnthropicAPIKey string
	AppPasswordHash string
	SessionSecret   string

	// Storage + server.
	DBPath string
	Listen string

	// Cloudflare Access (optional; enables tunnel JWT verification when set).
	CFAccessTeamDomain string
	CFAccessAUD        string

	// Seed defaults for live settings (DB overrides these once running).
	Defaults Settings
}

// Settings are the live, dashboard-editable operational settings. The env seeds
// them on first boot; afterwards the database is authoritative.
type Settings struct {
	DryRun              bool
	Timezone            string
	DigestHour          int
	DigestEnabled       bool
	PollInterval        time.Duration
	ConfidenceThreshold float64
	LLMDailyCap         int
	Model               string
	Privacy             PrivacyMode
}

// minPollInterval is the floor enforced on the poll interval to stay friendly to
// the JMAP API. The dashboard enforces the same bound.
const minPollInterval = 5 * time.Minute

// Load reads configuration from the environment, applying defaults and
// validating required secrets. Missing required secrets are reported together.
func Load() (*Config, error) {
	c := &Config{
		FastmailToken:      os.Getenv("FASTMAIL_TOKEN"),
		AnthropicAPIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		AppPasswordHash:    os.Getenv("APP_PASSWORD_HASH"),
		SessionSecret:      os.Getenv("SESSION_SECRET"),
		DBPath:             envOr("WINNOW_DB", "/data/winnow.db"),
		Listen:             envOr("WINNOW_LISTEN", "0.0.0.0:8080"),
		CFAccessTeamDomain: os.Getenv("CF_ACCESS_TEAM_DOMAIN"),
		CFAccessAUD:        os.Getenv("CF_ACCESS_AUD"),
	}

	c.Defaults = Settings{
		DryRun:              envBool("DRY_RUN", true),
		Timezone:            envOr("TZ", "UTC"),
		DigestHour:          envInt("DIGEST_HOUR", 7),
		DigestEnabled:       envBool("DIGEST_ENABLED", true),
		PollInterval:        envDuration("POLL_INTERVAL", 15*time.Minute),
		ConfidenceThreshold: envFloat("CONFIDENCE_THRESHOLD", 0.75),
		LLMDailyCap:         envInt("LLM_DAILY_CAP", 2000),
		Model:               envOr("ANTHROPIC_MODEL", "claude-haiku-4-5"),
		Privacy:             PrivacyMode(envOr("PRIVACY_MODE", string(PrivacySnippet))),
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	var missing []string
	for _, f := range []struct {
		name, val string
	}{
		{"FASTMAIL_TOKEN", c.FastmailToken},
		{"ANTHROPIC_API_KEY", c.AnthropicAPIKey},
		{"APP_PASSWORD_HASH", c.AppPasswordHash},
		{"SESSION_SECRET", c.SessionSecret},
	} {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if c.Defaults.PollInterval < minPollInterval {
		return fmt.Errorf("POLL_INTERVAL must be >= %s", minPollInterval)
	}
	if c.Defaults.ConfidenceThreshold < 0 || c.Defaults.ConfidenceThreshold > 1 {
		return errors.New("CONFIDENCE_THRESHOLD must be between 0 and 1")
	}
	switch c.Defaults.Privacy {
	case PrivacySnippet, PrivacySubjectSender:
	default:
		return fmt.Errorf("PRIVACY_MODE must be %q or %q", PrivacySnippet, PrivacySubjectSender)
	}
	return nil
}

// CFAccessEnabled reports whether tunnel JWT verification is configured.
func (c *Config) CFAccessEnabled() bool {
	return c.CFAccessTeamDomain != "" && c.CFAccessAUD != ""
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
