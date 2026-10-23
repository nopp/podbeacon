package diagnose

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagnoseParsesOutputText(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "id": "resp_test",
		  "status": "completed",
		  "output": [
		    {"type": "reasoning", "summary": []},
		    {"type": "message", "role": "assistant", "content": [
		      {"type": "output_text", "text": "{\"summary\":\"O container morre por OOM e volta em CrashLoop.\",\"confidence\":\"alta\",\"evidence\":[\"exit 137\"],\"next_checks\":[\"comparar limite e uso de memória\"]}"}
		    ]}
		  ]
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key", "grok-4.7", srv.Client())
	diag, err := c.Diagnose(context.Background(), "Pod: default/api")
	if err != nil {
		t.Fatal(err)
	}
	if diag.Summary == "" || diag.Confidence != "alta" || len(diag.Evidence) != 1 || len(diag.NextChecks) != 1 {
		t.Fatalf("%+v", diag)
	}
	if got["model"] != "grok-4.7" {
		t.Fatalf("model %#v", got["model"])
	}
	if got["store"] != false {
		t.Fatalf("store %#v", got["store"])
	}
	input, _ := got["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input %#v", got["input"])
	}
	user, _ := input[1].(map[string]any)
	if !strings.Contains(user["content"].(string), "default/api") {
		t.Fatalf("user content %#v", user)
	}
}

func TestDiagnoseOutOfCredits(t *testing.T) {
	bodies := []struct {
		status int
		body   string
		want   bool
	}{
		{status: http.StatusPaymentRequired, body: `{"error":"payment required"}`, want: true},
		{status: http.StatusTooManyRequests, body: `{"error":{"message":"Your team has either used all its credits or reached its monthly spending limit."}}`, want: true},
		{status: http.StatusTooManyRequests, body: `{"error":{"message":"Rate limit exceeded","code":"rate_limit"}}`, want: false},
	}
	for _, tt := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(tt.body))
		}))
		c := New(srv.URL, "k", "grok-4.7", srv.Client())
		_, err := c.Diagnose(context.Background(), "pod")
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected error", tt.status)
		}
		if errors.Is(err, ErrOutOfCredits) != tt.want {
			t.Fatalf("status %d body %s: out of credits=%v, err=%v", tt.status, tt.body, tt.want, err)
		}
	}
}

func TestDiagnoseHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(srv.URL, "k", "grok-4.7", srv.Client())
	if _, err := c.Diagnose(context.Background(), "pod"); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeUnknownConfidence(t *testing.T) {
	got := normalize(Diagnosis{Summary: "x", Confidence: "altissima", Evidence: []string{"  a  ", ""}})
	if got.Confidence != "baixa" || len(got.Evidence) != 1 || got.Evidence[0] != "a" {
		t.Fatalf("%+v", got)
	}
}
