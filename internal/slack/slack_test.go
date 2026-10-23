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
	text := Render(Alert{
		Namespace:  "default",
		Name:       "api",
		Signal:     "CrashLoopBackOff no container api",
		Summary:    "O container morre por falta de memória.",
		Confidence: "media",
		Evidence:   []string{"exit 137"},
		NextChecks: []string{"ver o limite de memória"},
	})
	for _, want := range []string{"default/api", "média", "falta de memória", "exit 137", "limite de memória"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestRenderPlainOmitsHypothesis(t *testing.T) {
	text := RenderPlain("payments", "api", "CrashLoopBackOff no container api")
	for _, want := range []string{"payments/api", "CrashLoopBackOff no container api"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	for _, absent := range []string{"Causa", "Checar", "Indícios", "Hipótese"} {
		if strings.Contains(text, absent) {
			t.Fatalf("plain message contains %q: %s", absent, text)
		}
	}
}

func TestWebhook(t *testing.T) {
	var got payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	c := New(srv.URL, "", "", srv.Client())
	if err := c.Send(context.Background(), "olá"); err != nil {
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
		var got payload
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
	err := c.Send(context.Background(), "olá")
	if err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("err %v", err)
	}
}
