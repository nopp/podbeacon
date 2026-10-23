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
	Image      string
	Phase      string
	Node       string
	Restarts   int32
	Summary    string
	Confidence string
	NextChecks []string
	Plain      bool
}

// Message is one Slack post. The channel shows a short colored card.
// Text is the notification line. Red is a problem, green is resolved,
// gray means the pod is gone.
type Message struct {
	Channel     string       `json:"channel,omitempty"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	UnfurlLinks bool         `json:"unfurl_links"`
	UnfurlMedia bool         `json:"unfurl_media"`
	Preview     string       `json:"-"`
}

// Attachment is the colored card. The bar is the state: the text inside stays short.
type Attachment struct {
	Color    string  `json:"color,omitempty"`
	Fallback string  `json:"fallback,omitempty"`
	Blocks   []Block `json:"blocks,omitempty"`
}

const (
	colorProblem  = "#E01E5A"
	colorResolved = "#2EB67D"
	colorDeleted  = "#616061"
)

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

// Render builds one short note. Facts sit on two lines under the pod name.
// A plain note stops there.
func Render(a Alert) Message {
	title := clip(oneLine(podTitle(a.Namespace, a.Name)), 140)
	if title == "" {
		title = "pod"
	}
	problem := clip(shortProblem(a.Problem), 120)
	image := shortImage(a.Image)
	summary := clip(oneLine(a.Summary), 220)
	checks := cleanList(a.NextChecks, 3, 120)
	meta := metaLine(oneLine(a.Phase), oneLine(a.Node), a.Restarts)

	var head strings.Builder
	fmt.Fprintf(&head, "*%s*", escapeMrkdwn(title))
	if line := problemLine(problem, image); line != "" {
		fmt.Fprintf(&head, "\n%s", line)
	}
	if meta != "" {
		fmt.Fprintf(&head, "\n%s", escapeMrkdwn(meta))
	}
	blocks := []Block{{Type: "section", Text: mrkdwn(head.String())}}

	var body strings.Builder
	fmt.Fprintf(&body, "*%s*\n", title)
	if problem != "" {
		fmt.Fprintf(&body, "%s\n", problem)
	}
	if image != "" {
		fmt.Fprintf(&body, "%s\n", image)
	}
	if meta != "" {
		fmt.Fprintf(&body, "%s\n", meta)
	}

	if !a.Plain {
		if summary == "" {
			summary = "No clear cause from the data collected."
		}
		label := confidenceLabel(a.Confidence)
		var cause strings.Builder
		fmt.Fprintf(&cause, "*Likely cause · %s*\n%s", label, escapeMrkdwn(summary))
		for _, item := range checks {
			fmt.Fprintf(&cause, "\n• %s", escapeMrkdwn(item))
		}
		blocks = append(blocks, Block{Type: "section", Text: mrkdwn(cause.String())})
		fmt.Fprintf(&body, "*Likely cause (%s):* %s\n", label, summary)
		for _, item := range checks {
			fmt.Fprintf(&body, "• %s\n", item)
		}
	}

	line := title
	if problem != "" {
		line += " — " + problem
	}
	return message(line, body.String(), colorProblem, blocks)
}

// RenderClosed builds the card sent when an alerted problem is over.
// gone means the pod left the API or is being deleted. Otherwise it recovered.
func RenderClosed(a Alert, gone bool) Message {
	title := clip(oneLine(podTitle(a.Namespace, a.Name)), 140)
	if title == "" {
		title = "pod"
	}
	problem := clip(shortProblem(a.Problem), 120)
	status := "Resolved"
	line := title + " — resolved"
	color := colorResolved
	if gone {
		status = "Deleted"
		line = title + " — deleted"
		color = colorDeleted
	}
	text := status
	if problem != "" {
		text += " · " + escapeMrkdwn(problem)
	}
	body := fmt.Sprintf("*%s*\n%s", title, status)
	if problem != "" {
		body += " · " + problem
	}
	head := fmt.Sprintf("*%s*\n%s", escapeMrkdwn(title), text)
	return message(line, body, color, []Block{{Type: "section", Text: mrkdwn(head)}})
}

func problemLine(problem, image string) string {
	switch {
	case problem != "" && image != "":
		return escapeMrkdwn(problem) + " · " + codeSpan(image)
	case problem != "":
		return escapeMrkdwn(problem)
	case image != "":
		return codeSpan(image)
	default:
		return ""
	}
}

func metaLine(phase, node string, restarts int32) string {
	var parts []string
	if phase != "" {
		parts = append(parts, phase)
	}
	if node != "" {
		parts = append(parts, node)
	}
	switch {
	case restarts == 1:
		parts = append(parts, "1 restart")
	case restarts > 1:
		parts = append(parts, fmt.Sprintf("%d restarts", restarts))
	}
	return strings.Join(parts, " · ")
}

func shortImage(s string) string {
	s = oneLine(s)
	if i := strings.LastIndex(s, "/"); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return clip(s, 80)
}

func message(line, preview, color string, blocks []Block) Message {
	line = clip(line, 200)
	return Message{
		Text:    line,
		Preview: strings.TrimRight(preview, "\n"),
		Attachments: []Attachment{{
			Color:    color,
			Fallback: line,
			Blocks:   blocks,
		}},
		UnfurlLinks: false,
		UnfurlMedia: false,
	}
}

func shortProblem(s string) string {
	s = oneLine(s)
	if i := strings.Index(s, ": "); i > 0 && i <= 80 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func codeSpan(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	return "`" + escapeMrkdwn(s) + "`"
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

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func escapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
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
	case "alta", "high":
		return "high"
	case "media", "medium":
		return "medium"
	default:
		return "low"
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
		return fmt.Errorf("send to slack: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read the slack response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned %s: %s", resp.Status, clip(string(respBody), 200))
	}
	if token == "" {
		return nil
	}
	var decoded struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return fmt.Errorf("decode the slack response: %w", err)
	}
	if !decoded.OK {
		if decoded.Error == "" {
			decoded.Error = "response without ok"
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
