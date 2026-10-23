// Package config reads the process environment for podwatch.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultModel   = "grok-4.7"
	DefaultBaseURL = "https://api.x.ai/v1"
)

// Config is the process configuration. Parse does not check that a Slack
// destination exists; call Validate after flags are applied.
type Config struct {
	XAIKey        string
	XAIModel      string
	XAIBaseURL    string
	XAITimeout    time.Duration
	SlackWebhook  string
	SlackBotToken string
	SlackChannel  string

	Namespaces        []string
	ExcludeNamespaces []string
	PollInterval      time.Duration
	Cooldown          time.Duration
	Grace             time.Duration
	TailLines         int
	MaxLogBytes       int
	MaxAlerts         int
	SinglePass        bool
	DryRun            bool
}

// Parse reads environment variables and applies defaults.
func Parse() (Config, error) {
	var cfg Config
	var err error
	cfg.XAIKey = strings.TrimSpace(os.Getenv("XAI_API_KEY"))
	cfg.XAIModel = strings.TrimSpace(os.Getenv("XAI_MODEL"))
	if cfg.XAIModel == "" {
		cfg.XAIModel = DefaultModel
	}
	cfg.XAIBaseURL = strings.TrimSpace(os.Getenv("XAI_BASE_URL"))
	if cfg.XAIBaseURL == "" {
		cfg.XAIBaseURL = DefaultBaseURL
	}
	cfg.XAITimeout, err = durEnv("XAI_TIMEOUT", 180*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.SlackWebhook = strings.TrimSpace(os.Getenv("SLACK_WEBHOOK_URL"))
	cfg.SlackBotToken = strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	cfg.SlackChannel = strings.TrimSpace(os.Getenv("SLACK_CHANNEL"))
	cfg.Namespaces = csv(os.Getenv("NAMESPACES"))
	if v, ok := os.LookupEnv("EXCLUDE_NAMESPACES"); ok {
		cfg.ExcludeNamespaces = csv(v)
	} else {
		cfg.ExcludeNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}
	}
	cfg.PollInterval, err = durEnv("POLL_INTERVAL", time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.Cooldown, err = durEnv("ALERT_COOLDOWN", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.Grace, err = durEnv("STARTUP_GRACE", 2*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.TailLines, err = intEnv("LOG_TAIL_LINES", 80)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxLogBytes, err = intEnv("MAX_LOG_BYTES", 12_000)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxAlerts, err = intEnv("MAX_ALERTS_PER_SCAN", 8)
	if err != nil {
		return Config{}, err
	}
	cfg.SinglePass, err = boolEnv("ONCE")
	if err != nil {
		return Config{}, err
	}
	cfg.DryRun, err = boolEnv("DRY_RUN")
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// WithFlags turns on single-pass or dry-run when the command line asks for them.
func (c Config) WithFlags(single, dry bool) Config {
	if single {
		c.SinglePass = true
	}
	if dry {
		c.DryRun = true
	}
	return c
}

// Validate checks the fields required to run.
func (c Config) Validate() error {
	if c.XAIKey == "" {
		return fmt.Errorf("missing XAI_API_KEY")
	}
	if err := httpsURL("XAI_BASE_URL", c.XAIBaseURL); err != nil {
		return err
	}
	if c.DryRun {
		return nil
	}
	hasWebhook := c.SlackWebhook != ""
	hasBot := c.SlackBotToken != "" || c.SlackChannel != ""
	switch {
	case hasWebhook && hasBot:
		return fmt.Errorf("set only SLACK_WEBHOOK_URL, or both SLACK_BOT_TOKEN and SLACK_CHANNEL")
	case hasWebhook:
		return httpsURL("SLACK_WEBHOOK_URL", c.SlackWebhook)
	case c.SlackBotToken != "" && c.SlackChannel != "":
		return nil
	default:
		return fmt.Errorf("missing SLACK_WEBHOOK_URL, or SLACK_BOT_TOKEN and SLACK_CHANNEL")
	}
}

func httpsURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s must be an https URL", name)
	}
	return nil
}

func durEnv(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration, such as %s", key, def)
	}
	return d, nil
}

func intEnv(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return n, nil
}

func boolEnv(key string) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "sim":
		return true, nil
	case "0", "false", "no", "n", "nao", "não":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", key)
	}
}

func csv(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
