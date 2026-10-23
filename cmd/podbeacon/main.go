package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agent-ai/internal/agent"
	"agent-ai/internal/config"
	"agent-ai/internal/dedup"
	"agent-ai/internal/diagnose"
	"agent-ai/internal/k8s"
	"agent-ai/internal/slack"
)

func main() {
	once := flag.Bool("once", false, "scan once and exit")
	dry := flag.Bool("dry-run", false, "print the message and do not call Slack")
	flag.Parse()

	cfg, err := config.Parse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	cfg = cfg.WithFlags(*once, *dry)
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	logger.Info("podbeacon", "headline", "kubernetes Pod Health & Alerting Agent")
	client, err := k8s.NewClientset()
	if err != nil {
		logger.Error("kubernetes", "err", err)
		os.Exit(1)
	}
	scanner := k8s.NewScanner(client, k8s.Options{
		Include:     cfg.Namespaces,
		Exclude:     cfg.ExcludeNamespaces,
		Grace:       cfg.Grace,
		TailLines:   int64(cfg.TailLines),
		MaxLogBytes: int64(cfg.MaxLogBytes),
	})
	model := diagnose.New(cfg.XAIBaseURL, cfg.XAIKey, cfg.XAIModel, &http.Client{Timeout: cfg.XAITimeout})

	var notify func(context.Context, slack.Message) error
	if cfg.DryRun {
		notify = slack.DryRun(os.Stdout)
	} else {
		notify = slack.New(cfg.SlackWebhook, cfg.SlackBotToken, cfg.SlackChannel, &http.Client{Timeout: 15 * time.Second}).Send
	}

	app := &agent.Agent{
		List:       scanner.List,
		Present:    scanner.Present,
		Enrich:     scanner.Enrich,
		Diagnose:   model.Diagnose,
		Notify:     notify,
		Dedup:      dedup.New(cfg.Cooldown),
		Max:        cfg.MaxAlerts,
		Every:      cfg.PollInterval,
		SinglePass: cfg.SinglePass,
		Log:        logger,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil {
		logger.Error("stopped", "err", err)
		os.Exit(1)
	}
}
