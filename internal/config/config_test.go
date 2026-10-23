package config

import (
	"os"
	"testing"
	"time"
)

func TestDefaultsAndValidate(t *testing.T) {
	scrub(t)
	t.Setenv("XAI_API_KEY", "xai-test")
	t.Setenv("SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/T00/B00/xxx")

	cfg, err := Parse()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.XAIModel != DefaultModel {
		t.Fatalf("model %s", cfg.XAIModel)
	}
	if cfg.PollInterval != time.Minute || cfg.Cooldown != 30*time.Minute || cfg.Grace != 2*time.Minute {
		t.Fatalf("intervals %+v", cfg)
	}
	if len(cfg.ExcludeNamespaces) != 3 {
		t.Fatalf("exclude %+v", cfg.ExcludeNamespaces)
	}
}

func TestDryRunSkipsSlack(t *testing.T) {
	scrub(t)
	t.Setenv("XAI_API_KEY", "xai-test")
	cfg, err := Parse()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.WithFlags(true, true)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.DryRun || !cfg.SinglePass {
		t.Fatalf("flags not applied: %+v", cfg)
	}
}

func TestRejectsBothSlackModes(t *testing.T) {
	scrub(t)
	t.Setenv("XAI_API_KEY", "xai-test")
	t.Setenv("SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/T/B/x")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-1")
	t.Setenv("SLACK_CHANNEL", "C123")
	cfg, err := Parse()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestEmptyExcludeMeansNone(t *testing.T) {
	scrub(t)
	t.Setenv("XAI_API_KEY", "xai-test")
	t.Setenv("DRY_RUN", "true")
	t.Setenv("EXCLUDE_NAMESPACES", "")
	cfg, err := Parse()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExcludeNamespaces != nil {
		t.Fatalf("exclude %+v", cfg.ExcludeNamespaces)
	}
}

func TestBadDuration(t *testing.T) {
	scrub(t)
	t.Setenv("POLL_INTERVAL", "logo")
	if _, err := Parse(); err == nil {
		t.Fatal("expected error")
	}
}

func scrub(t *testing.T) {
	t.Helper()
	keys := []string{
		"XAI_API_KEY", "XAI_MODEL", "XAI_BASE_URL", "XAI_TIMEOUT",
		"SLACK_WEBHOOK_URL", "SLACK_BOT_TOKEN", "SLACK_CHANNEL",
		"NAMESPACES", "EXCLUDE_NAMESPACES", "POLL_INTERVAL", "ALERT_COOLDOWN",
		"STARTUP_GRACE", "LOG_TAIL_LINES", "MAX_LOG_BYTES", "MAX_ALERTS_PER_SCAN",
		"ONCE", "DRY_RUN",
	}
	for _, key := range keys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}
