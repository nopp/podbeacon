package k8s

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFormatEvent(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	got := formatEvent(corev1.Event{
		Type:          "Warning",
		Reason:        "BackOff",
		Message:       "back-off restarting failed container",
		Count:         4,
		LastTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
	}, now)
	if !strings.Contains(got, "2m Warning BackOff (x4)") {
		t.Fatalf("%q", got)
	}
}

func TestHumanDur(t *testing.T) {
	if humanDur(45*time.Second) != "45s" || humanDur(3*time.Minute) != "3m" || humanDur(2*time.Hour) != "2h" || humanDur(72*time.Hour) != "3d" {
		t.Fatalf("45s=%s 3m=%s 2h=%s 3d=%s", humanDur(45*time.Second), humanDur(3*time.Minute), humanDur(2*time.Hour), humanDur(72*time.Hour))
	}
}
