package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"agent-ai/internal/dedup"
	"agent-ai/internal/diagnose"
	"agent-ai/internal/k8s"
	"agent-ai/internal/slack"
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
		Notify: func(_ context.Context, msg slack.Message) error {
			calls++
			if calls == 1 {
				return errors.New("slack down")
			}
			if !strings.Contains(msg.Preview, "default/api") || !strings.Contains(msg.Preview, "Crash no startup") {
				t.Fatalf("text %s", msg.Preview)
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
		Notify: func(_ context.Context, got slack.Message) error {
			text = got.Preview
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

func TestScanPlainMessageWhenCreditsRunOut(t *testing.T) {
	var texts []string
	var diagnoses int
	app := &Agent{
		List: func(context.Context) ([]k8s.Report, error) {
			return []k8s.Report{
				{Namespace: "default", Name: "api", Severity: 100, Reasons: []k8s.Reason{{Code: "c:api:CrashLoopBackOff", Text: "CrashLoopBackOff no container api"}}},
				{Namespace: "default", Name: "worker", Severity: 90, Reasons: []k8s.Reason{{Code: "c:worker:ImagePullBackOff", Text: "ImagePullBackOff no container worker"}}},
			}, nil
		},
		Diagnose: func(context.Context, string) (diagnose.Diagnosis, error) {
			diagnoses++
			return diagnose.Diagnosis{}, fmt.Errorf("chamada: %w", diagnose.ErrOutOfCredits)
		},
		Notify: func(_ context.Context, msg slack.Message) error {
			texts = append(texts, msg.Preview)
			return nil
		},
		Dedup: dedup.New(time.Minute),
		Now:   func() time.Time { return time.Unix(0, 0) },
	}
	sent, _, err := app.Scan(context.Background())
	if err != nil || sent != 2 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	if diagnoses != 1 {
		t.Fatalf("diagnose calls=%d", diagnoses)
	}
	for _, text := range texts {
		if strings.Contains(text, "Causa") || strings.Contains(text, "Checar") || strings.Contains(text, "Hipótese") || strings.Contains(text, "análise automática") {
			t.Fatalf("suggestion in %s", text)
		}
	}
	if !strings.Contains(texts[0], "default/api") || !strings.Contains(texts[0], "CrashLoopBackOff") {
		t.Fatalf("first %s", texts[0])
	}
	if !strings.Contains(texts[1], "default/worker") || !strings.Contains(texts[1], "ImagePullBackOff") {
		t.Fatalf("second %s", texts[1])
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
		Notify: func(_ context.Context, msg slack.Message) error {
			names = append(names, msg.Preview)
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

func TestScanSaysFixedWhenTheProblemClears(t *testing.T) {
	report := problemReport("payments", "api", "c:api:CrashLoopBackOff", "CrashLoopBackOff no container api")
	step := 0
	var diagnoses int
	var previews []string
	app := openAgent(t, &step, &diagnoses, &previews, report)
	app.Present = func(context.Context) (map[string]k8s.Presence, error) {
		return map[string]k8s.Presence{"payments/api": {}}, nil
	}
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	step = 1
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if diagnoses != 1 || len(previews) != 2 {
		t.Fatalf("diagnoses=%d previews=%#v", diagnoses, previews)
	}
	if !strings.Contains(previews[1], "Corrigido") || !strings.Contains(previews[1], "CrashLoopBackOff") || strings.Contains(previews[1], "Causa") {
		t.Fatalf("close %s", previews[1])
	}
	step = 0
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if diagnoses != 2 {
		t.Fatalf("problem should alert again after it cleared, diagnoses=%d", diagnoses)
	}
}

func TestScanSaysGoneWhenThePodLeaves(t *testing.T) {
	report := problemReport("payments", "api", "c:api:CrashLoopBackOff", "CrashLoopBackOff no container api")
	step := 0
	var diagnoses int
	var previews []string
	app := openAgent(t, &step, &diagnoses, &previews, report)
	app.Present = func(context.Context) (map[string]k8s.Presence, error) {
		if step == 0 {
			return map[string]k8s.Presence{"payments/api": {}}, nil
		}
		return map[string]k8s.Presence{}, nil
	}
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	step = 1
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 || !strings.Contains(previews[1], "Não existe mais") || strings.Contains(previews[1], "Corrigido") {
		t.Fatalf("previews %#v", previews)
	}
	if diagnoses != 1 {
		t.Fatalf("diagnoses=%d", diagnoses)
	}
}

func TestScanSaysGoneWhenThePodIsDeleting(t *testing.T) {
	report := problemReport("payments", "api", "c:api:CrashLoopBackOff", "CrashLoopBackOff no container api")
	step := 0
	var diagnoses int
	var previews []string
	app := openAgent(t, &step, &diagnoses, &previews, report)
	app.Present = func(context.Context) (map[string]k8s.Presence, error) {
		return map[string]k8s.Presence{"payments/api": {Deleting: step > 0}}, nil
	}
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	step = 1
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 || !strings.Contains(previews[1], "Não existe mais") {
		t.Fatalf("previews %#v", previews)
	}
}

func TestScanStaysOpenWhileThePodIsStillUnhealthy(t *testing.T) {
	first := problemReport("payments", "api", "c:api:CrashLoopBackOff", "CrashLoopBackOff no container api")
	second := problemReport("payments", "api", "c:api:ImagePullBackOff", "ImagePullBackOff no container api")
	step := 0
	var diagnoses int
	var previews []string
	app := openAgent(t, &step, &diagnoses, &previews, first)
	app.List = func(context.Context) ([]k8s.Report, error) {
		switch step {
		case 0:
			return []k8s.Report{first}, nil
		case 1:
			return []k8s.Report{second}, nil
		default:
			return nil, nil
		}
	}
	app.Present = live("payments/api")
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	step = 1
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, preview := range previews {
		if strings.Contains(preview, "Corrigido") || strings.Contains(preview, "Não existe mais") {
			t.Fatalf("closed while still unhealthy: %#v", previews)
		}
	}
	if diagnoses != 2 {
		t.Fatalf("diagnoses=%d", diagnoses)
	}
	step = 2
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(previews) != 3 || !strings.Contains(previews[2], "Corrigido") || !strings.Contains(previews[2], "ImagePullBackOff") {
		t.Fatalf("previews %#v", previews)
	}
}

func TestScanRetriesAFailedClosingMessage(t *testing.T) {
	report := problemReport("default", "api", "c:api:CrashLoopBackOff", "CrashLoopBackOff no container api")
	step := 0
	var diagnoses int
	var previews []string
	app := openAgent(t, &step, &diagnoses, &previews, report)
	app.Present = live("default/api")
	fails := 0
	app.Notify = func(_ context.Context, msg slack.Message) error {
		previews = append(previews, msg.Preview)
		if strings.Contains(msg.Preview, "Corrigido") && fails == 0 {
			fails++
			return errors.New("slack down")
		}
		return nil
	}
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	step = 1
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := 0
	for _, preview := range previews {
		if strings.Contains(preview, "Corrigido") {
			closed++
		}
	}
	if closed != 2 {
		t.Fatalf("want a failed close and a retry, previews %#v", previews)
	}
}

func TestScanSkipsClosingWhenPresenceFails(t *testing.T) {
	report := problemReport("default", "api", "c:api:CrashLoopBackOff", "CrashLoopBackOff no container api")
	step := 0
	var diagnoses int
	var previews []string
	app := openAgent(t, &step, &diagnoses, &previews, report)
	app.Present = func(context.Context) (map[string]k8s.Presence, error) {
		if step == 1 {
			return nil, errors.New("api down")
		}
		return map[string]k8s.Presence{"default/api": {}}, nil
	}
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	step = 1
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(previews) != 1 {
		t.Fatalf("presence failure must not close or fail the scan: %#v", previews)
	}
	step = 2
	if _, _, err := app.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 || !strings.Contains(previews[1], "Corrigido") {
		t.Fatalf("previews %#v", previews)
	}
}

func problemReport(namespace, name, code, text string) k8s.Report {
	return k8s.Report{
		Namespace: namespace,
		Name:      name,
		Severity:  100,
		Reasons:   []k8s.Reason{{Code: code, Text: text, Container: "api"}},
	}
}

func live(key string) func(context.Context) (map[string]k8s.Presence, error) {
	return func(context.Context) (map[string]k8s.Presence, error) {
		return map[string]k8s.Presence{key: {}}, nil
	}
}

func openAgent(t *testing.T, step, diagnoses *int, previews *[]string, report k8s.Report) *Agent {
	t.Helper()
	return &Agent{
		List: func(context.Context) ([]k8s.Report, error) {
			if *step == 0 {
				return []k8s.Report{report}, nil
			}
			return nil, nil
		},
		Diagnose: func(context.Context, string) (diagnose.Diagnosis, error) {
			*diagnoses++
			return diagnose.Diagnosis{Summary: "causa", Confidence: "baixa"}, nil
		},
		Notify: func(_ context.Context, msg slack.Message) error {
			*previews = append(*previews, msg.Preview)
			return nil
		},
		Dedup: dedup.New(time.Hour),
		Now:   func() time.Time { return time.Unix(0, 0) },
	}
}
