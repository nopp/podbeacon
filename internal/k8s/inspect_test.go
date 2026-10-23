package k8s

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInspect(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	grace := 2 * time.Minute

	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
		code string
	}{
		{name: "ready", pod: readyPod(now), want: false},
		{name: "crashloop during grace", pod: crashLoopPod(now, 10*time.Second, "back-off 5s"), want: true, code: "c:api:CrashLoopBackOff"},
		{name: "oom", pod: terminatedPod(now, "OOMKilled", 137), want: true, code: "c:api:OOMKilled"},
		{name: "previous oom while running", pod: previousOOMPod(now), want: false},
		{name: "image pull during grace", pod: waitingPod(now, 10*time.Second, "ImagePullBackOff", "not found"), want: true, code: "c:api:ImagePullBackOff"},
		{name: "err image pull shares the pull code", pod: waitingPod(now, 10*time.Second, "ErrImagePull", "not found"), want: true, code: "c:api:ImagePullBackOff"},
		{name: "container creating during grace", pod: waitingPod(now, 10*time.Second, "ContainerCreating", ""), want: false},
		{name: "container creating after grace", pod: waitingPod(now, 5*time.Minute, "ContainerCreating", ""), want: true, code: "c:api:ContainerCreating"},
		{name: "unschedulable during grace", pod: unschedulablePod(now, 10*time.Second), want: false},
		{name: "unschedulable after grace", pod: unschedulablePod(now, 5*time.Minute), want: true, code: "pod:Unschedulable"},
		{name: "succeeded", pod: phasePod(now, corev1.PodSucceeded), want: false},
		{name: "deleting crashloop", pod: deleting(crashLoopPod(now, 5*time.Minute, "back-off")), want: false},
		{name: "init crashloop", pod: initCrashPod(now), want: true, code: "init:migrate:CrashLoopBackOff"},
		{name: "config error", pod: waitingPod(now, 10*time.Second, "CreateContainerConfigError", `secret "db" not found`), want: true, code: "c:api:CreateContainerConfigError"},
		{name: "evicted", pod: evictedPod(now), want: true, code: "pod:Failed:Evicted"},
		{name: "pending after grace", pod: phaseAge(now, 5*time.Minute, corev1.PodPending), want: true, code: "pod:Pending"},
		{name: "not ready after grace", pod: notReadyPod(now), want: true, code: "pod:NotReady"},
		{name: "unknown", pod: phasePod(now, corev1.PodUnknown), want: true, code: "pod:Unknown"},
		{name: "init still running", pod: initRunningPod(now), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, bad := Inspect(tt.pod, now, grace)
			if bad != tt.want {
				t.Fatalf("problema=%v, want %v", bad, tt.want)
			}
			if tt.code == "" {
				return
			}
			if !hasCode(got, tt.code) {
				t.Fatalf("codes=%v, want %s", codesOf(got), tt.code)
			}
		})
	}
}

func TestFingerprintIgnoresBackoffMessage(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	a, _ := Inspect(crashLoopPod(now, 5*time.Minute, "back-off 5s"), now, time.Minute)
	b, _ := Inspect(crashLoopPod(now, 5*time.Minute, "back-off 5m0s"), now, time.Minute)
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("%s vs %s", a.Fingerprint(), b.Fingerprint())
	}
	if a.Reasons[0].Text == b.Reasons[0].Text {
		t.Fatal("text should keep the backoff message")
	}
}

func TestEvidenceIncludesLastOOM(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	pod := crashLoopPod(now, 5*time.Minute, "back-off 5m0s restarting failed container")
	pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
	}
	pod.Status.ContainerStatuses[0].RestartCount = 7
	got, bad := Inspect(pod, now, time.Minute)
	if !bad {
		t.Fatal("expected a problem")
	}
	text := got.Evidence()
	for _, want := range []string{"CrashLoopBackOff", "OOMKilled", "default/api", "reinícios=7"} {
		if !strings.Contains(text, want) {
			t.Fatalf("evidence missing %q:\n%s", want, text)
		}
	}
	if got.Signal() == "" || !strings.Contains(got.Signal(), "7 reinícios") {
		t.Fatalf("signal %q", got.Signal())
	}
}

func hasCode(r Report, code string) bool {
	for _, reason := range r.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func codesOf(r Report) []string {
	out := make([]string, len(r.Reasons))
	for i, reason := range r.Reasons {
		out[i] = reason.Code
	}
	return out
}

func basePod(now time.Time, age time.Duration) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "api",
			Namespace:         "default",
			UID:               "uid-1",
			CreationTimestamp: metav1.NewTime(now.Add(-age)),
			Labels:            map[string]string{"app.kubernetes.io/name": "api"},
			OwnerReferences:   []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-abc"}},
		},
		Spec: corev1.PodSpec{
			NodeName:   "node-a",
			Containers: []corev1.Container{{Name: "api", Image: "ghcr.io/acme/api:1"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, QOSClass: corev1.PodQOSBurstable},
	}
}

func readyPod(now time.Time) *corev1.Pod {
	pod := basePod(now, 10*time.Minute)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "api", Ready: true, Image: "ghcr.io/acme/api:1",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	return pod
}

func crashLoopPod(now time.Time, age time.Duration, message string) *corev1.Pod {
	pod := basePod(now, age)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "api", Ready: false, RestartCount: 4, Image: "ghcr.io/acme/api:1",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: message}},
	}}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"}}
	return pod
}

func waitingPod(now time.Time, age time.Duration, reason, message string) *corev1.Pod {
	pod := basePod(now, age)
	pod.Status.Phase = corev1.PodPending
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "api", Ready: false, Image: "ghcr.io/acme/api:1",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
	}}
	return pod
}

func terminatedPod(now time.Time, reason string, exit int32) *corev1.Pod {
	pod := basePod(now, 5*time.Minute)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "api", Ready: false,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: exit}},
	}}
	return pod
}

func previousOOMPod(now time.Time) *corev1.Pod {
	pod := readyPod(now)
	pod.Status.ContainerStatuses[0].RestartCount = 3
	pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
	}
	return pod
}

func unschedulablePod(now time.Time, age time.Duration) *corev1.Pod {
	pod := basePod(now, age)
	pod.Status.Phase = corev1.PodPending
	pod.Spec.NodeName = ""
	pod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
		Reason: corev1.PodReasonUnschedulable, Message: "0/3 nodes are available: insufficient cpu",
	}}
	return pod
}

func phasePod(now time.Time, phase corev1.PodPhase) *corev1.Pod {
	return phaseAge(now, 10*time.Minute, phase)
}

func phaseAge(now time.Time, age time.Duration, phase corev1.PodPhase) *corev1.Pod {
	pod := basePod(now, age)
	pod.Status.Phase = phase
	return pod
}

func notReadyPod(now time.Time) *corev1.Pod {
	pod := basePod(now, 5*time.Minute)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "api", Ready: false, Image: "ghcr.io/acme/api:1",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	return pod
}

func deleting(pod *corev1.Pod) *corev1.Pod {
	ts := metav1.NewTime(pod.CreationTimestamp.Time.Add(time.Minute))
	pod.DeletionTimestamp = &ts
	return pod
}

func initCrashPod(now time.Time) *corev1.Pod {
	pod := basePod(now, time.Minute)
	pod.Spec.InitContainers = []corev1.Container{{Name: "migrate", Image: "ghcr.io/acme/migrate:1"}}
	pod.Status.Phase = corev1.PodPending
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: "migrate", Ready: false, RestartCount: 2,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"}},
	}}
	return pod
}

func initRunningPod(now time.Time) *corev1.Pod {
	pod := basePod(now, 5*time.Minute)
	pod.Status.Phase = corev1.PodPending
	pod.Spec.InitContainers = []corev1.Container{{Name: "migrate", Image: "ghcr.io/acme/migrate:1"}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:  "migrate",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	return pod
}

func evictedPod(now time.Time) *corev1.Pod {
	pod := basePod(now, 5*time.Minute)
	pod.Status.Phase = corev1.PodFailed
	pod.Status.Reason = "Evicted"
	pod.Status.Message = "the node was low on resource: memory"
	return pod
}
