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
		Phase:      "Running",
		Node:       "node-a",
		Restarts:   7,
		Summary:    "O container morre por falta de memória.",
		Confidence: "media",
		NextChecks: []string{"ver o limite de memória"},
	})
	for _, want := range []string{"default/api", "CrashLoopBackOff", "Running", "node-a", "7", "média", "falta de memória", "limite de memória"} {
		if !strings.Contains(msg.Preview, want) {
			t.Fatalf("missing %q in %s", want, msg.Preview)
		}
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"attachments"`, `"header"`, "#E01E5A", "Causa provável", "*CrashLoopBackOff no container api*"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %q in %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "*Problema*") {
		t.Fatalf("problem should be its own line, not a field: %s", raw)
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
	for _, absent := range []string{"Causa", "Checar", "Hipótese"} {
		if strings.Contains(string(raw), absent) {
			t.Fatalf("plain card contains %q: %s", absent, raw)
		}
	}
	if !strings.Contains(string(raw), "#ECB22E") {
		t.Fatalf("plain color: %s", raw)
	}
}

func TestRenderClosed(t *testing.T) {
	fixed := RenderClosed(Alert{
		Namespace: "payments",
		Name:      "api",
		Problem:   "CrashLoopBackOff no container api",
	}, false)
	if !strings.Contains(fixed.Preview, "Corrigido") || !strings.Contains(fixed.Preview, "CrashLoopBackOff") {
		t.Fatalf("fixed %s", fixed.Preview)
	}
	raw, err := json.Marshal(fixed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "#2EB67D") || strings.Contains(string(raw), "Causa") || strings.Contains(string(raw), "Hipótese") {
		t.Fatalf("fixed json %s", raw)
	}

	gone := RenderClosed(Alert{Namespace: "payments", Name: "api", Problem: "CrashLoopBackOff no container api"}, true)
	if !strings.Contains(gone.Preview, "Não existe mais") || strings.Contains(gone.Preview, "Corrigido") {
		t.Fatalf("gone %s", gone.Preview)
	}
	raw, err = json.Marshal(gone)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "#616061") {
		t.Fatalf("gone color %s", raw)
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
	if err := c.Send(context.Background(), Message{Text: "olá"}); err != nil {
		t.Fatal(err)
	}
	if got.Text != "olá" || got.Channel != "" {
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
	err := c.Send(context.Background(), Message{Text: "olá"})
	if err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("err %v", err)
	}
}
