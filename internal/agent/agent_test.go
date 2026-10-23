package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-ai/internal/dedup"
	"agent-ai/internal/diagnose"
	"agent-ai/internal/k8s"
)

func TestScanDedupsAndRetriesSlack(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	report := k8s.Report{
		Namespace: "default",
		Name:      "api",
		Phase:     "Running",
		Severity:  100,
		Reasons: []k8s.Reason{{
			Code: "c:api:CrashLoopBackOff", Text: "CrashLoopBackOff no container api", Severity: 100, Container: "api",
		}},
	}
	var calls int
	app := &Agent{
		List: func(context.Context) ([]k8s.Report, error) {
			return []k8s.Report{report}, nil
		},
		Enrich: func(_ context.Context, in k8s.Report) (k8s.Report, error) {
			in.Events = []string{"1m Warning BackOff (x4) back-off"}
			return in, nil
		},
		Diagnose: func(_ context.Context, evidence string) (diagnose.Diagnosis, error) {
			if !strings.Contains(evidence, "BackOff") {
				t.Fatalf("evidence was not enriched: %s", evidence)
			}
			return diagnose.Diagnosis{Summary: "Crash no startup.", Confidence: "alta", Evidence: []string{"BackOff"}}, nil
		},
		Notify: func(_ context.Context, text string) error {
			calls++
			if calls == 1 {
				return errors.New("slack down")
			}
			if !strings.Contains(text, "default/api") || !strings.Contains(text, "Crash no startup") {
				t.Fatalf("text %s", text)
			}
			return nil
		},
		Dedup: dedup.New(30 * time.Minute),
		Now:   func() time.Time { return now },
		Max:   8,
	}
	sent, found, err := app.Scan(context.Background())
	if err != nil || found != 1 || sent != 0 {
		t.Fatalf("sent=%d found=%d err=%v", sent, found, err)
	}
	sent, _, err = app.Scan(context.Background())
	if err != nil || sent != 1 {
		t.Fatalf("retry sent=%d err=%v", sent, err)
	}
	sent, _, err = app.Scan(context.Background())
	if err != nil || sent != 0 {
		t.Fatalf("dedup sent=%d err=%v", sent, err)
	}
}

func TestScanFallsBackWhenModelFails(t *testing.T) {
	var text string
	app := &Agent{
		List: func(context.Context) ([]k8s.Report, error) {
			return []k8s.Report{{
				Namespace: "default", Name: "api", Severity: 90,
				Reasons: []k8s.Reason{{Code: "c:api:ImagePullBackOff", Text: "ImagePullBackOff no container api"}},
			}}, nil
		},
		Diagnose: func(context.Context, string) (diagnose.Diagnosis, error) {
			return diagnose.Diagnosis{}, errors.New("timeout")
		},
		Notify: func(_ context.Context, got string) error {
			text = got
			return nil
		},
		Dedup: dedup.New(time.Minute),
		Now:   func() time.Time { return time.Unix(0, 0) },
	}
	sent, _, err := app.Scan(context.Background())
	if err != nil || sent != 1 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	if !strings.Contains(text, "análise automática falhou") {
		t.Fatalf("text %s", text)
	}
}

func TestScanKeepsTheWorstPods(t *testing.T) {
	var names []string
	app := &Agent{
		List: func(context.Context) ([]k8s.Report, error) {
			return []k8s.Report{
				{Namespace: "default", Name: "low", Severity: 10, Reasons: []k8s.Reason{{Code: "pod:Pending", Text: "Pending"}}},
				{Namespace: "default", Name: "high", Severity: 100, Reasons: []k8s.Reason{{Code: "c:api:CrashLoopBackOff", Text: "CrashLoop"}}},
			}, nil
		},
		Diagnose: func(context.Context, string) (diagnose.Diagnosis, error) {
			return diagnose.Diagnosis{Summary: "causa", Confidence: "baixa"}, nil
		},
		Notify: func(_ context.Context, text string) error {
			names = append(names, text)
			return nil
		},
		Dedup: dedup.New(time.Minute),
		Now:   func() time.Time { return time.Unix(0, 0) },
		Max:   1,
	}
	sent, found, err := app.Scan(context.Background())
	if err != nil || found != 2 || sent != 1 {
		t.Fatalf("sent=%d found=%d err=%v", sent, found, err)
	}
	if len(names) != 1 || !strings.Contains(names[0], "default/high") {
		t.Fatalf("names %#v", names)
	}
}
