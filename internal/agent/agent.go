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
	List     func(context.Context) ([]k8s.Report, error)
	Enrich   func(context.Context, k8s.Report) (k8s.Report, error)
	Diagnose func(context.Context, string) (diagnose.Diagnosis, error)
	Notify   func(context.Context, slack.Message) error
	// Present lists pods still in scope, including healthy ones.
	// A nil Present skips the closing message.
	Present    func(context.Context) (map[string]k8s.Presence, error)
	Dedup      *dedup.Store
	Now        func() time.Time
	Max        int
	Every      time.Duration
	SinglePass bool
	Log        *slog.Logger

	// open is the pods whose problem card was delivered and not closed yet.
	open map[string]openPod
}

// openPod is one problem card still waiting for the pod to recover or leave.
type openPod struct {
	namespace    string
	name         string
	problem      string
	fingerprints []string
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
	if err := a.closeOpen(ctx, items); err != nil {
		return sent, len(items), err
	}
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
		msg, confidence, msgErr := a.message(ctx, report, &skipModel)
		if msgErr != nil {
			return sent, len(items), msgErr
		}
		if err := a.Notify(ctx, msg); err != nil {
			a.logger().Error("slack", "pod", report.Key(), "err", err)
			continue
		}
		a.dedup().Mark(report.Fingerprint(), now)
		a.noteOpen(report)
		sent++
		a.logger().Info("alerta enviado", "pod", report.Key(), "confiança", confidence)
	}
	return sent, len(items), nil
}

// closeOpen tells Slack when an alerted pod is healthy or gone.
// A Slack failure keeps the pod open so the next scan retries.
// A failure to list pods skips this scan, so a blip is not reported as a deletion.
func (a *Agent) closeOpen(ctx context.Context, problems []k8s.Report) error {
	if a.Present == nil || len(a.open) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	present, err := a.Present(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.logger().Warn("presença dos pods", "err", err)
		return nil
	}
	still := make(map[string]struct{}, len(problems))
	for _, item := range problems {
		still[item.Key()] = struct{}{}
	}
	keys := make([]string, 0, len(a.open))
	for key := range a.open {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := still[key]; ok {
			continue
		}
		inc := a.open[key]
		view, ok := present[key]
		gone := !ok || view.Deleting
		msg := slack.RenderClosed(slack.Alert{
			Namespace: inc.namespace,
			Name:      inc.name,
			Problem:   inc.problem,
		}, gone)
		if err := a.Notify(ctx, msg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.logger().Error("slack", "pod", key, "err", err)
			continue
		}
		for _, fp := range inc.fingerprints {
			a.dedup().Forget(fp)
		}
		delete(a.open, key)
		estado := "corrigido"
		if gone {
			estado = "não existe mais"
		}
		a.logger().Info("problema encerrado", "pod", key, "estado", estado)
	}
	return nil
}

func (a *Agent) noteOpen(report k8s.Report) {
	if a.open == nil {
		a.open = map[string]openPod{}
	}
	key := report.Key()
	inc := a.open[key]
	inc.namespace = report.Namespace
	inc.name = report.Name
	inc.problem = redact.String(report.Headline())
	fp := report.Fingerprint()
	seen := false
	for _, have := range inc.fingerprints {
		if have == fp {
			seen = true
			break
		}
	}
	if !seen {
		inc.fingerprints = append(inc.fingerprints, fp)
	}
	a.open[key] = inc
}

// message builds the Slack card. When skipModel is set, the card is only the
// pod and its problem. A credit failure sets skipModel for the caller.
func (a *Agent) message(ctx context.Context, report k8s.Report, skipModel *bool) (slack.Message, string, error) {
	plain := slack.Render(scrub(card(report, diagnose.Diagnosis{}, true)))
	if *skipModel {
		return plain, "sem créditos", nil
	}
	evidence := redact.String(report.Evidence())
	diag, err := a.Diagnose(ctx, evidence)
	if err != nil {
		if ctx.Err() != nil {
			return slack.Message{}, "", ctx.Err()
		}
		if errors.Is(err, diagnose.ErrOutOfCredits) {
			*skipModel = true
			a.logger().Error("créditos da IA esgotados", "pod", report.Key(), "err", err)
			return plain, "sem créditos", nil
		}
		a.logger().Error("diagnóstico", "pod", report.Key(), "err", err)
		diag = diagnose.Fallback(report.Signal())
	}
	return slack.Render(scrub(card(report, diag, false))), diag.Confidence, nil
}

func card(report k8s.Report, diag diagnose.Diagnosis, plain bool) slack.Alert {
	return slack.Alert{
		Namespace:  report.Namespace,
		Name:       report.Name,
		Problem:    report.Headline(),
		Image:      report.Image(),
		Phase:      report.Phase,
		Node:       report.Node,
		Restarts:   report.Restarts(),
		Summary:    diag.Summary,
		Confidence: diag.Confidence,
		NextChecks: diag.NextChecks,
		Plain:      plain,
	}
}

func scrub(a slack.Alert) slack.Alert {
	a.Problem = redact.String(a.Problem)
	a.Image = redact.String(a.Image)
	a.Phase = redact.String(a.Phase)
	a.Node = redact.String(a.Node)
	a.Summary = redact.String(a.Summary)
	for i := range a.NextChecks {
		a.NextChecks[i] = redact.String(a.NextChecks[i])
	}
	return a
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
