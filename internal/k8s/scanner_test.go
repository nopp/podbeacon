package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListSkipsHealthyAndExcluded(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	bad := crashLoopPod(now, 5*time.Minute, "back-off")
	healthy := readyPod(now)
	healthy.Name = "web"
	system := crashLoopPod(now, 5*time.Minute, "back-off")
	system.Namespace = "kube-system"
	system.Name = "kube-proxy"
	other := crashLoopPod(now, 5*time.Minute, "back-off")
	other.Namespace = "payments"
	other.Name = "worker"

	cs := fake.NewClientset(bad, healthy, system, other)
	s := NewScanner(cs, Options{
		Exclude: []string{"kube-system"},
		Grace:   2 * time.Minute,
		Now:     func() time.Time { return now },
	})
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gotKeys := map[string]bool{}
	for _, key := range keys(got) {
		gotKeys[key] = true
	}
	if len(got) != 2 || !gotKeys["default/api"] || !gotKeys["payments/worker"] {
		t.Fatalf("got %#v", keys(got))
	}
}

func TestEnrichRecordsLogFailure(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	pod := crashLoopPod(now, 5*time.Minute, "back-off")
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "api.back", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Name: pod.Name, UID: pod.UID, Namespace: pod.Namespace,
		},
		Type:          "Warning",
		Reason:        "BackOff",
		Message:       "back-off restarting failed container",
		Count:         4,
		LastTimestamp: metav1.NewTime(now.Add(-time.Minute)),
	}
	cs := fake.NewClientset(pod, ev)
	s := NewScanner(cs, Options{Grace: time.Minute, Now: func() time.Time { return now }})
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list %d", len(list))
	}
	got, err := s.Enrich(context.Background(), list[0])
	if err != nil {
		t.Fatal(err)
	}
	text := got.Evidence()
	if !strings.Contains(text, "BackOff") {
		t.Fatalf("events missing from evidence:\n%s", text)
	}
	if !strings.Contains(text, "leitura indisponível") && !strings.Contains(text, "Logs:") {
		t.Fatalf("expected a log note:\n%s", text)
	}
}

func keys(reports []Report) []string {
	out := make([]string, len(reports))
	for i, r := range reports {
		out[i] = r.Key()
	}
	return out
}
