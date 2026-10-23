package redact

import "testing"

func TestString(t *testing.T) {
	in := "password=supersecret token: abc api_key=\"zzz\" Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U AKIAIOSFODNN7EXAMPLE xoxb-123-abc secret \"db-password\" not found"
	got := String(in)
	for _, secret := range []string{"supersecret", "eyJhbGciOi", "AKIAIOSFODNN7EXAMPLE", "xoxb-123-abc", "zzz"} {
		if contains(got, secret) {
			t.Errorf("still contains %q in %q", secret, got)
		}
	}
	if !contains(got, `secret "db-password" not found`) {
		t.Fatalf("secret name was redacted: %s", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
