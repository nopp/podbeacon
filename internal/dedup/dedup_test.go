package dedup

import (
	"testing"
	"time"
)

func TestCooldown(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := New(30 * time.Minute)
	if !s.Allow("default/api|c:api:CrashLoopBackOff", now) {
		t.Fatal("first sighting should send")
	}
	s.Mark("default/api|c:api:CrashLoopBackOff", now)
	if s.Allow("default/api|c:api:CrashLoopBackOff", now.Add(29*time.Minute)) {
		t.Fatal("inside the cooldown")
	}
	if !s.Allow("default/api|c:api:CrashLoopBackOff", now.Add(30*time.Minute)) {
		t.Fatal("cooldown elapsed")
	}
	if !s.Allow("other/api|c:api:CrashLoopBackOff", now) {
		t.Fatal("a different pod is independent")
	}
}
