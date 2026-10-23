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
// Plain omits the cause and the checks.
type Alert struct {
	Namespace  string
	Name       string
	Problem    string
	Phase      string
	Node       string
	Restarts   int32
	Summary    string
	Confidence string
	NextChecks []string
	Plain      bool
}

// Message is a Slack card. Text is the notification fallback. Preview is the
// readable form used by dry-run and is not sent.
type Message struct {
	Channel     string       `json:"channel,omitempty"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Preview     string       `json:"-"`
}

// Attachment is the colored card Slack shows in the channel.
// Fallback is the plain line used by clients that cannot render the card.
type Attachment struct {
	Color    string  `json:"color,omitempty"`
	Fallback string  `json:"fallback,omitempty"`
	Blocks   []Block `json:"blocks,omitempty"`
}

// Block is one Block Kit element. Unused fields stay out of the JSON.
type Block struct {
	Type     string       `json:"type"`
	Text     *TextObject  `json:"text,omitempty"`
	Fields   []TextObject `json:"fields,omitempty"`
	Elements []TextObject `json:"elements,omitempty"`
}

// TextObject is a Block Kit text node.
type TextObject struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// Render builds a short card. The problem is full width. Phase, node, and
// restarts sit on the row under it. A plain card stops there.
func Render(a Alert) Message {
	title := clip(oneLine(podTitle(a.Namespace, a.Name)), 140)
	if title == "" {
		title = "pod"
	}
	problem := clip(oneLine(a.Problem), 200)
	summary := clip(oneLine(a.Summary), 280)
	checks := cleanList(a.NextChecks, 3, 140)
	phase := oneLine(a.Phase)
	node := oneLine(a.Node)

	blocks := []Block{{
		Type: "header",
		Text: &TextObject{Type: "plain_text", Text: title, Emoji: true},
	}}
	if problem != "" {
		blocks = append(blocks, Block{Type: "section", Text: mrkdwn("*" + escapeMrkdwn(problem) + "*")})
	}

	var fields []TextObject
	if phase != "" {
		fields = append(fields, field("Fase", phase))
	}
	if node != "" {
		fields = append(fields, field("Node", node))
	}
	if a.Restarts > 0 {
		fields = append(fields, field("Reinícios", fmt.Sprintf("%d", a.Restarts)))
	}
	if len(fields) > 0 {
		blocks = append(blocks, Block{Type: "section", Fields: fields})
	}

	var body strings.Builder
	fmt.Fprintf(&body, "*%s*\n", title)
	writePreview(&body, "Problema", problem)
	writePreview(&body, "Fase", phase)
	writePreview(&body, "Node", node)
	if a.Restarts > 0 {
		writePreview(&body, "Reinícios", fmt.Sprintf("%d", a.Restarts))
	}

	color := "#E01E5A"
	if !a.Plain {
		if summary == "" {
			summary = "Sem uma causa clara a partir dos dados coletados."
		}
		label := confidenceLabel(a.Confidence)
		blocks = append(blocks,
			Block{Type: "divider"},
			Block{Type: "section", Text: mrkdwn(fmt.Sprintf("*Causa provável · %s*\n%s", label, escapeMrkdwn(summary)))},
		)
		fmt.Fprintf(&body, "*Causa provável (%s):* %s\n", label, summary)
		if len(checks) > 0 {
			var list strings.Builder
			list.WriteString("*Checar*\n")
			body.WriteString("*Checar:*\n")
			for _, item := range checks {
				fmt.Fprintf(&list, "• %s\n", escapeMrkdwn(item))
				fmt.Fprintf(&body, "• %s\n", item)
			}
			blocks = append(blocks, Block{Type: "section", Text: mrkdwn(strings.TrimRight(list.String(), "\n"))})
		}
		blocks = append(blocks, Block{
			Type:     "context",
			Elements: []TextObject{{Type: "mrkdwn", Text: "Hipótese do modelo. Confira antes de agir."}},
		})
	} else {
		color = "#ECB22E"
	}

	line := title
	if problem != "" {
		line += " — " + problem
	}
	return Message{
		Text:    clip(line, 200),
		Preview: strings.TrimRight(body.String(), "\n"),
		Attachments: []Attachment{{
			Color:    color,
			Fallback: clip(line, 200),
			Blocks:   blocks,
		}},
	}
}

// RenderClosed builds the card sent when an alerted problem is over.
// gone means the pod left the API or is being deleted. Otherwise it recovered.
func RenderClosed(a Alert, gone bool) Message {
	title := clip(oneLine(podTitle(a.Namespace, a.Name)), 140)
	if title == "" {
		title = "pod"
	}
	problem := clip(oneLine(a.Problem), 200)
	status := "Corrigido"
	detail := "O problema não está mais neste pod."
	color := "#2EB67D"
	line := title + " — corrigido"
	if gone {
		status = "Não existe mais"
		detail = "O pod não está mais no cluster."
		color = "#616061"
		line = title + " — não existe mais"
	}

	blocks := []Block{
		{Type: "header", Text: &TextObject{Type: "plain_text", Text: title, Emoji: true}},
		{Type: "section", Text: mrkdwn(fmt.Sprintf("*%s*\n%s", status, detail))},
	}
	if problem != "" {
		blocks = append(blocks, Block{Type: "section", Text: mrkdwn("*Problema*\n" + escapeMrkdwn(problem))})
	}

	var body strings.Builder
	fmt.Fprintf(&body, "*%s*\n", title)
	writePreview(&body, "Estado", status)
	writePreview(&body, "Problema", problem)
	return Message{
		Text:    clip(line, 200),
		Preview: strings.TrimRight(body.String(), "\n"),
		Attachments: []Attachment{{
			Color:    color,
			Fallback: clip(line, 200),
			Blocks:   blocks,
		}},
	}
}

func mrkdwn(text string) *TextObject {
	return &TextObject{Type: "mrkdwn", Text: text}
}

func podTitle(namespace, name string) string {
	switch {
	case namespace != "" && name != "":
		return namespace + "/" + name
	case name != "":
		return name
	default:
		return namespace
	}
}

func field(label, value string) TextObject {
	return TextObject{Type: "mrkdwn", Text: fmt.Sprintf("*%s*\n%s", label, escapeMrkdwn(value))}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func escapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func writePreview(b *strings.Builder, label, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "*%s:* %s\n", label, value)
}

func cleanList(items []string, n, width int) []string {
	var out []string
	for _, item := range items {
		item = clip(oneLine(item), width)
		if item == "" {
			continue
		}
		out = append(out, item)
		if len(out) == n {
			break
		}
	}
	return out
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

// Send posts the card. WebhookURL wins when it is set.
func (c *Client) Send(ctx context.Context, msg Message) error {
	endpoint := c.WebhookURL
	var token string
	if endpoint == "" {
		endpoint = c.APIURL
		if endpoint == "" {
			endpoint = "https://slack.com/api/chat.postMessage"
		}
		msg.Channel = c.Channel
		token = c.BotToken
	}
	raw, err := json.Marshal(msg)
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

// DryRun prints the card and does not call Slack.
func DryRun(w io.Writer) func(context.Context, Message) error {
	return func(ctx context.Context, msg Message) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body := msg.Preview
		if body == "" {
			body = msg.Text
		}
		_, err := fmt.Fprintln(w, body)
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
