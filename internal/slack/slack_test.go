package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	msg := Render(Alert{
		Namespace:  "default",
		Name:       "api",
		Problem:    "CrashLoopBackOff no container api",
		Image:      "docker.io/library/nginx:9",
		Phase:      "Running",
		Node:       "node-a",
		Restarts:   7,
		Summary:    "The container is running out of memory.",
		Confidence: "media",
		NextChecks: []string{"check the memory limit"},
	})
	for _, want := range []string{"default/api", "CrashLoopBackOff", "Running", "node-a", "7 restarts", "medium", "running out of memory", "memory limit"} {
		if !strings.Contains(msg.Preview, want) {
			t.Fatalf("missing %q in %s", want, msg.Preview)
		}
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"blocks"`, "Likely cause", "CrashLoopBackOff no container api", "`nginx:9`", `"unfurl_links":false`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %q in %s", want, raw)
		}
	}
	for _, absent := range []string{`"attachments"`, `"header"`, ":red_circle:", "rpc error", `"fields"`} {
		if strings.Contains(string(raw), absent) {
			t.Fatalf("card contains %q: %s", absent, raw)
		}
	}
}

func TestRenderDropsRawAPIError(t *testing.T) {
	msg := Render(Alert{
		Namespace: "default",
		Name:      "nginx",
		Problem:   `ErrImagePull no container nginx: rpc error: code = NotFound desc = failed to pull image "docker.io/library/nginx:9"`,
		Image:     "docker.io/library/nginx:9",
	})
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "rpc error") || strings.Contains(msg.Preview, "rpc error") {
		t.Fatalf("raw error leaked: %s", raw)
	}
	if !strings.Contains(string(raw), "ErrImagePull no container nginx") {
		t.Fatalf("headline missing: %s", raw)
	}
}

func TestRenderPlainOmitsHypothesis(t *testing.T) {
	msg := Render(Alert{
		Namespace: "payments",
		Name:      "api",
		Problem:   "CrashLoopBackOff no container api",
		Plain:     true,
	})
	for _, want := range []string{"payments/api", "CrashLoopBackOff no container api"} {
		if !strings.Contains(msg.Preview, want) {
			t.Fatalf("missing %q in %s", want, msg.Preview)
		}
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"Likely cause", "Resolved", "Deleted"} {
		if strings.Contains(string(raw), absent) {
			t.Fatalf("plain card contains %q: %s", absent, raw)
		}
	}
	if strings.Contains(string(raw), `"attachments"`) || strings.Contains(string(raw), ":large_yellow_circle:") {
		t.Fatalf("plain card: %s", raw)
	}
}

func TestRenderClosed(t *testing.T) {
	fixed := RenderClosed(Alert{
		Namespace: "payments",
		Name:      "api",
		Problem:   "CrashLoopBackOff no container api",
	}, false)
	if !strings.Contains(fixed.Preview, "Resolved") || !strings.Contains(fixed.Preview, "CrashLoopBackOff") {
		t.Fatalf("fixed %s", fixed.Preview)
	}
	raw, err := json.Marshal(fixed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Likely cause") || strings.Contains(string(raw), `"attachments"`) || !strings.Contains(string(raw), "Resolved") {
		t.Fatalf("fixed json %s", raw)
	}

	gone := RenderClosed(Alert{Namespace: "payments", Name: "api", Problem: "CrashLoopBackOff no container api"}, true)
	if !strings.Contains(gone.Preview, "Deleted") || strings.Contains(gone.Preview, "Resolved") {
		t.Fatalf("gone %s", gone.Preview)
	}
	raw, err = json.Marshal(gone)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Deleted") {
		t.Fatalf("gone mark %s", raw)
	}
}

func TestWebhook(t *testing.T) {
	var got Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	c := New(srv.URL, "", "", srv.Client())
	if err := c.Send(context.Background(), Message{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if got.Text != "hello" || got.Channel != "" {
		t.Fatalf("%+v", got)
	}
}

func TestBotRejectsSlackError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		var got Message
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got.Channel != "C123" {
			t.Errorf("channel %s", got.Channel)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":false,"error":"not_in_channel"}`)
	}))
	defer srv.Close()
	c := New("", "xoxb-test", "C123", srv.Client())
	c.APIURL = srv.URL
	err := c.Send(context.Background(), Message{Text: "hello"})
	if err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("err %v", err)
	}
}
