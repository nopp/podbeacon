// Package agent ties a pod scan, a model diagnosis and a Slack notification.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"agent-ai/internal/dedup"
	"agent-ai/internal/diagnose"
	"agent-ai/internal/k8s"
	"agent-ai/internal/redact"
	"agent-ai/internal/slack"
)

// Agent runs one scan or a poll loop.
type Agent struct {
	List       func(context.Context) ([]k8s.Report, error)
	Enrich     func(context.Context, k8s.Report) (k8s.Report, error)
	Diagnose   func(context.Context, string) (diagnose.Diagnosis, error)
	Notify     func(context.Context, string) error
	Dedup      *dedup.Store
	Now        func() time.Time
	Max        int
	Every      time.Duration
	SinglePass bool
	Log        *slog.Logger
}

// Run scans until the context is canceled. SinglePass returns after one scan.
func (a *Agent) Run(ctx context.Context) error {
	every := a.Every
	if every <= 0 {
		every = time.Minute
	}
	for {
		n, found, err := a.Scan(ctx)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil
		case err != nil && a.SinglePass:
			return err
		case err != nil:
			a.logger().Error("varredura", "err", err)
		default:
			a.logger().Info("varredura concluída", "problemas", found, "alertas", n)
		}
		if a.SinglePass {
			return nil
		}
		timer := time.NewTimer(every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// Scan sends at most Max alerts. A Slack failure is not marked, so the next
// scan tries that pod again. A model failure still notifies, with a fallback.
func (a *Agent) Scan(ctx context.Context) (sent int, found int, err error) {
	items, err := a.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Severity != items[j].Severity {
			return items[i].Severity > items[j].Severity
		}
		return items[i].Key() < items[j].Key()
	})
	now := a.now()
	attempts := 0
	// One exhausted-credit response covers the rest of this scan. The next
	// poll tries the model again, so a top-up starts producing hypotheses
	// without a restart.
	skipModel := false
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return sent, len(items), err
		}
		if !a.dedup().Allow(item.Fingerprint(), now) {
			continue
		}
		if attempts >= a.max() {
			a.logger().Info("limite de alertas da varredura", "max", a.max())
			break
		}
		attempts++
		report := item
		if !skipModel && a.Enrich != nil {
			enriched, enrichErr := a.Enrich(ctx, item)
			if enrichErr != nil {
				if ctx.Err() != nil {
					return sent, len(items), ctx.Err()
				}
				a.logger().Warn("detalhes do pod", "pod", item.Key(), "err", enrichErr)
			} else {
				report = enriched
			}
		}
		text, confidence, msgErr := a.message(ctx, report, &skipModel)
		if msgErr != nil {
			return sent, len(items), msgErr
		}
		if err := a.Notify(ctx, text); err != nil {
			a.logger().Error("slack", "pod", report.Key(), "err", err)
			continue
		}
		a.dedup().Mark(report.Fingerprint(), now)
		sent++
		a.logger().Info("alerta enviado", "pod", report.Key(), "confiança", confidence)
	}
	return sent, len(items), nil
}

// message builds the Slack text. When skipModel is set, the text is only the
// pod and its problem. A credit failure sets skipModel for the caller.
func (a *Agent) message(ctx context.Context, report k8s.Report, skipModel *bool) (string, string, error) {
	plain := redact.String(slack.RenderPlain(report.Namespace, report.Name, report.Signal()))
	if *skipModel {
		return plain, "sem créditos", nil
	}
	evidence := redact.String(report.Evidence())
	diag, err := a.Diagnose(ctx, evidence)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		if errors.Is(err, diagnose.ErrOutOfCredits) {
			*skipModel = true
			a.logger().Error("créditos da IA esgotados", "pod", report.Key(), "err", err)
			return plain, "sem créditos", nil
		}
		a.logger().Error("diagnóstico", "pod", report.Key(), "err", err)
		diag = diagnose.Fallback(report.Signal())
	}
	return redact.String(slack.Render(slack.Alert{
		Namespace:  report.Namespace,
		Name:       report.Name,
		Signal:     report.Signal(),
		Summary:    diag.Summary,
		Confidence: diag.Confidence,
		Evidence:   diag.Evidence,
		NextChecks: diag.NextChecks,
	})), diag.Confidence, nil
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Agent) max() int {
	if a.Max <= 0 {
		return 8
	}
	return a.Max
}

func (a *Agent) dedup() *dedup.Store {
	if a.Dedup == nil {
		a.Dedup = dedup.New(30 * time.Minute)
	}
	return a.Dedup
}

func (a *Agent) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}
