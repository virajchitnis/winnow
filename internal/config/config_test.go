package config

import (
	"strings"
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("FASTMAIL_TOKEN", "tok")
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("APP_PASSWORD_HASH", "hash")
	t.Setenv("SESSION_SECRET", "secret")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Defaults.PollInterval != 15*time.Minute {
		t.Errorf("PollInterval = %s, want 15m", c.Defaults.PollInterval)
	}
	if !c.Defaults.DryRun {
		t.Error("DryRun should default true")
	}
	if c.Defaults.Privacy != PrivacySnippet {
		t.Errorf("Privacy = %q, want snippet", c.Defaults.Privacy)
	}
	if c.CFAccessEnabled() {
		t.Error("CF Access should be disabled when unset")
	}
}

func TestLoadMissingSecrets(t *testing.T) {
	// Ensure none are set.
	t.Setenv("FASTMAIL_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("APP_PASSWORD_HASH", "")
	t.Setenv("SESSION_SECRET", "")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing secrets")
	}
	for _, want := range []string{"FASTMAIL_TOKEN", "ANTHROPIC_API_KEY", "APP_PASSWORD_HASH", "SESSION_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"poll too low", map[string]string{"POLL_INTERVAL": "1m"}, "POLL_INTERVAL"},
		{"bad confidence", map[string]string{"CONFIDENCE_THRESHOLD": "1.5"}, "CONFIDENCE_THRESHOLD"},
		{"bad privacy", map[string]string{"PRIVACY_MODE": "everything"}, "PRIVACY_MODE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCFAccessEnabled(t *testing.T) {
	setRequired(t)
	t.Setenv("CF_ACCESS_TEAM_DOMAIN", "team.cloudflareaccess.com")
	t.Setenv("CF_ACCESS_AUD", "aud123")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.CFAccessEnabled() {
		t.Error("CF Access should be enabled when both set")
	}
}
