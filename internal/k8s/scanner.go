package k8s

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	"agent-ai/internal/redact"
)

// Options selects which pods are scanned and how much log to keep.
type Options struct {
	Include     []string
	Exclude     []string
	Grace       time.Duration
	TailLines   int64
	MaxLogBytes int64
	Now         func() time.Time
}

// Presence is a pod still returned by the API in the scanned scope.
// Deleting is true when the pod has a deletion timestamp.
type Presence struct {
	Deleting bool
}

// Scanner lists unhealthy pods and attaches events and log tails.
type Scanner struct {
	client  kubernetes.Interface
	opt     Options
	exclude map[string]struct{}
}

func NewScanner(client kubernetes.Interface, opt Options) *Scanner {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.TailLines <= 0 {
		opt.TailLines = 80
	}
	if opt.MaxLogBytes <= 0 {
		opt.MaxLogBytes = 12_000
	}
	ex := make(map[string]struct{}, len(opt.Exclude))
	for _, ns := range opt.Exclude {
		ex[ns] = struct{}{}
	}
	return &Scanner{client: client, opt: opt, exclude: ex}
}

// List returns unhealthy pods without events or logs.
func (s *Scanner) List(ctx context.Context) ([]Report, error) {
	pods, err := s.podsInScope(ctx)
	if err != nil {
		return nil, err
	}
	now := s.opt.Now()
	var out []Report
	for i := range pods {
		report, bad := Inspect(&pods[i], now, s.opt.Grace)
		if !bad {
			continue
		}
		out = append(out, report)
	}
	return out, nil
}

// Present returns every pod in scope, including healthy ones.
// A pod that is being deleted stays in the map with Deleting set.
func (s *Scanner) Present(ctx context.Context) (map[string]Presence, error) {
	pods, err := s.podsInScope(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Presence, len(pods))
	for i := range pods {
		pod := &pods[i]
		out[pod.Namespace+"/"+pod.Name] = Presence{Deleting: pod.DeletionTimestamp != nil}
	}
	return out, nil
}

func (s *Scanner) podsInScope(ctx context.Context) ([]corev1.Pod, error) {
	namespaces := s.opt.Include
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	var out []corev1.Pod
	for _, ns := range namespaces {
		if ns != "" {
			if _, skip := s.exclude[ns]; skip {
				continue
			}
		}
		pods, err := s.listPods(ctx, ns)
		if err != nil {
			where := ns
			if where == "" {
				where = "all namespaces"
			}
			return nil, fmt.Errorf("list pods in %s: %w", where, err)
		}
		for i := range pods {
			if _, skip := s.exclude[pods[i].Namespace]; skip {
				continue
			}
			out = append(out, pods[i])
		}
	}
	return out, nil
}

// Enrich adds events and log tails. A failure to read either is recorded on
// the report so the scan can still describe the pod.
func (s *Scanner) Enrich(ctx context.Context, in Report) (Report, error) {
	if err := ctx.Err(); err != nil {
		return in, err
	}
	out := in
	events, err := s.events(ctx, in)
	if err != nil {
		if ctx.Err() != nil {
			return in, ctx.Err()
		}
		out.EventNote = clip(oneLine(err.Error()), 160)
	} else {
		out.Events = events
	}
	out.Logs = s.collectLogs(ctx, in)
	return out, nil
}

func (s *Scanner) listPods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	var out []corev1.Pod
	var cont string
	for {
		list, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			Limit:    500,
			Continue: cont,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, list.Items...)
		if list.Continue == "" {
			return out, nil
		}
		cont = list.Continue
	}
}

func (s *Scanner) events(ctx context.Context, r Report) ([]string, error) {
	sel := fields.AndSelectors(
		fields.OneTermEqualSelector("involvedObject.name", r.Name),
		fields.OneTermEqualSelector("involvedObject.uid", r.UID),
	).String()
	ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.client.CoreV1().Events(r.Namespace).List(ectx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		return nil, err
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		return eventTime(items[i]).Before(eventTime(items[j]))
	})
	if len(items) > 12 {
		items = items[len(items)-12:]
	}
	now := s.opt.Now()
	out := make([]string, 0, len(items))
	for _, e := range items {
		out = append(out, redact.String(formatEvent(e, now)))
	}
	return out, nil
}

func (s *Scanner) collectLogs(ctx context.Context, r Report) []LogSnippet {
	var out []LogSnippet
	for _, name := range r.LogContainers {
		if ctx.Err() != nil {
			return out
		}
		got := false
		if restarts(r, name) > 0 {
			text, err := s.readLog(ctx, r.Namespace, r.Name, name, true)
			if err == nil {
				if t := strings.TrimSpace(redact.String(text)); t != "" {
					out = append(out, LogSnippet{Container: name, Previous: true, Text: t})
					got = true
				}
			}
		}
		text, err := s.readLog(ctx, r.Namespace, r.Name, name, false)
		switch {
		case err != nil && !got:
			out = append(out, LogSnippet{Container: name, Note: clip(oneLine(err.Error()), 160)})
		case err == nil:
			if t := strings.TrimSpace(redact.String(text)); t != "" {
				out = append(out, LogSnippet{Container: name, Text: t})
			}
		}
	}
	return out
}

func (s *Scanner) readLog(ctx context.Context, ns, name, container string, previous bool) (string, error) {
	tail := s.opt.TailLines
	limit := s.opt.MaxLogBytes
	req := s.client.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{
		Container:  container,
		Previous:   previous,
		TailLines:  &tail,
		LimitBytes: &limit,
		Timestamps: true,
	})
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	stream, err := req.Stream(lctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	b, err := io.ReadAll(io.LimitReader(stream, limit))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func restarts(r Report, name string) int32 {
	for _, c := range r.Containers {
		if c.Name == name {
			return c.Restarts
		}
	}
	return 0
}
