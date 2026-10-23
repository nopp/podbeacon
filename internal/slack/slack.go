// Package slack publishes a pod alert to an incoming webhook or chat.postMessage.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Alert is the message content, already redacted by the caller.
type Alert struct {
	Namespace  string
	Name       string
	Signal     string
	Summary    string
	Confidence string
	Evidence   []string
	NextChecks []string
}

// Render builds a Slack mrkdwn message.
func Render(a Alert) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*Pod com problema:* `%s/%s`\n", a.Namespace, a.Name)
	if a.Signal != "" {
		fmt.Fprintf(&b, "*Sinal:* %s\n", a.Signal)
	}
	summary := strings.TrimSpace(a.Summary)
	if summary == "" {
		summary = "Sem uma causa clara a partir dos dados coletados."
	}
	fmt.Fprintf(&b, "*Causa provável (%s):* %s\n", confidenceLabel(a.Confidence), summary)
	writeBullets(&b, "Indícios", a.Evidence)
	writeBullets(&b, "Checar", a.NextChecks)
	b.WriteString("_Hipótese do modelo. Confira eventos e logs antes de agir._")
	return clip(b.String(), 3500)
}

func writeBullets(b *strings.Builder, title string, items []string) {
	var kept []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		return
	}
	fmt.Fprintf(b, "*%s:*\n", title)
	for _, item := range kept {
		fmt.Fprintf(b, "• %s\n", item)
	}
}

func confidenceLabel(c string) string {
	switch c {
	case "alta":
		return "alta"
	case "media":
		return "média"
	default:
		return "baixa"
	}
}

// Client sends text to Slack. APIURL overrides chat.postMessage in tests.
type Client struct {
	WebhookURL string
	BotToken   string
	Channel    string
	APIURL     string
	HTTP       *http.Client
}

func New(webhook, token, channel string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{WebhookURL: webhook, BotToken: token, Channel: channel, HTTP: httpClient}
}

type payload struct {
	Channel string `json:"channel,omitempty"`
	Text    string `json:"text"`
}

// Send posts text. WebhookURL wins when it is set.
func (c *Client) Send(ctx context.Context, text string) error {
	body := payload{Text: text}
	endpoint := c.WebhookURL
	var token string
	if endpoint == "" {
		endpoint = c.APIURL
		if endpoint == "" {
			endpoint = "https://slack.com/api/chat.postMessage"
		}
		body.Channel = c.Channel
		token = c.BotToken
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "podwatch/0.1")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("enviar ao slack: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("ler resposta do slack: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack respondeu %s: %s", resp.Status, clip(string(respBody), 200))
	}
	if token == "" {
		return nil
	}
	var decoded struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return fmt.Errorf("decodificar resposta do slack: %w", err)
	}
	if !decoded.OK {
		if decoded.Error == "" {
			decoded.Error = "resposta sem ok"
		}
		return fmt.Errorf("slack: %s", decoded.Error)
	}
	return nil
}

// DryRun prints the message and does not call Slack.
func DryRun(w io.Writer) func(context.Context, string) error {
	return func(ctx context.Context, text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w, text)
		return err
	}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
