// Package diagnose asks Grok, through the SpaceXSI Responses API, for a likely pod cause.
package diagnose

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

const (
	defaultModel   = "grok-4.7"
	defaultBaseURL = "https://api.x.ai/v1"
	maxEvidence    = 24_000
)

// Diagnosis is the structured hypothesis returned by the model.
type Diagnosis struct {
	Summary    string   `json:"summary"`
	Confidence string   `json:"confidence"`
	Evidence   []string `json:"evidence"`
	NextChecks []string `json:"next_checks"`
}

const systemPrompt = `Você é um engenheiro de plantão analisando um Pod do Kubernetes.
Use somente o relato enviado. Se a evidência não bastar, diga isso e use confiança baixa.
Não invente métrica, nome de Secret, linha de log ou evento que não esteja no relato.
Não sugira ação destrutiva (apagar recurso, reduzir réplicas a zero, remover volume, force delete).
next_checks são verificações, no máximo três.
Responda em português do Brasil.`

// diagnosisSchema is sent as text.format.schema on the Responses API.
const diagnosisSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "summary": {"type": "string", "maxLength": 500, "description": "Uma frase com a causa mais provável."},
    "confidence": {"type": "string", "enum": ["baixa", "media", "alta"]},
    "evidence": {
      "type": "array",
      "maxItems": 4,
      "items": {"type": "string", "maxLength": 300},
      "description": "Indícios que aparecem no relato."
    },
    "next_checks": {
      "type": "array",
      "maxItems": 3,
      "items": {"type": "string", "maxLength": 300},
      "description": "Verificações que um humano deve fazer. Sem comandos destrutivos."
    }
  },
  "required": ["summary", "confidence", "evidence", "next_checks"]
}`

// Client calls POST /v1/responses.
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

func New(baseURL, apiKey, model string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if model == "" {
		model = defaultModel
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 180 * time.Second}
	}
	return &Client{BaseURL: baseURL, APIKey: apiKey, Model: model, HTTP: httpClient}
}

type requestBody struct {
	Model           string         `json:"model"`
	Store           bool           `json:"store"`
	Temperature     float64        `json:"temperature"`
	MaxOutputTokens int            `json:"max_output_tokens"`
	Input           []inputMessage `json:"input"`
	Text            textFormat     `json:"text"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type textFormat struct {
	Format schemaFormat `json:"format"`
}

type schemaFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type responseBody struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

// Diagnose returns a hypothesis for evidence. The request sets store=false so
// the pod report is not retained by the provider.
func (c *Client) Diagnose(ctx context.Context, evidence string) (Diagnosis, error) {
	body := requestBody{
		Model:           c.Model,
		Store:           false,
		Temperature:     0.2,
		MaxOutputTokens: 800,
		Input: []inputMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: clip(evidence, maxEvidence)},
		},
		Text: textFormat{Format: schemaFormat{
			Type:   "json_schema",
			Name:   "pod_diagnosis",
			Strict: true,
			Schema: json.RawMessage(diagnosisSchema),
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Diagnosis{}, err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return Diagnosis{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("User-Agent", "podwatch/0.1")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("chamar o modelo: %w", err)
	}
	defer resp.Body.Close()
	respRaw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Diagnosis{}, fmt.Errorf("ler resposta do modelo: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Diagnosis{}, fmt.Errorf("modelo respondeu %s: %s", resp.Status, clip(string(respRaw), 300))
	}
	var decoded responseBody
	if err := json.Unmarshal(respRaw, &decoded); err != nil {
		return Diagnosis{}, fmt.Errorf("decodificar resposta do modelo: %w", err)
	}
	if msg := errorMessage(decoded.Error); msg != "" {
		return Diagnosis{}, fmt.Errorf("resposta do modelo: %s", msg)
	}
	text := outputText(decoded)
	if strings.TrimSpace(text) == "" {
		return Diagnosis{}, fmt.Errorf("modelo não devolveu texto (status %s, id %s)", decoded.Status, decoded.ID)
	}
	var diag Diagnosis
	if err := json.Unmarshal([]byte(extractJSON(text)), &diag); err != nil {
		return Diagnosis{}, fmt.Errorf("diagnóstico fora do formato: %w", err)
	}
	diag = normalize(diag)
	if diag.Summary == "" && len(diag.Evidence) == 0 && len(diag.NextChecks) == 0 {
		return Diagnosis{}, fmt.Errorf("diagnóstico vazio (id %s)", decoded.ID)
	}
	return diag, nil
}

// Fallback is used when the model call fails. The pod problem is still reported.
func Fallback(signal string) Diagnosis {
	d := Diagnosis{
		Summary:    "A análise automática falhou. Vale olhar os eventos e os logs deste pod.",
		Confidence: "baixa",
	}
	if signal != "" {
		d.Evidence = []string{clip(signal, 300)}
	}
	return d
}

func outputText(resp responseBody) string {
	var b strings.Builder
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				b.WriteString(part.Text)
			}
		}
	}
	return b.String()
}

func normalize(d Diagnosis) Diagnosis {
	d.Summary = clip(strings.TrimSpace(d.Summary), 500)
	switch d.Confidence {
	case "baixa", "media", "alta":
	default:
		d.Confidence = "baixa"
	}
	d.Evidence = clipList(d.Evidence, 4)
	d.NextChecks = clipList(d.NextChecks, 3)
	return d
}

func clipList(items []string, n int) []string {
	var out []string
	for _, item := range items {
		item = clip(strings.TrimSpace(item), 300)
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

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func errorMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var asObj struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &asObj); err == nil {
		return asObj.Message
	}
	return clip(string(raw), 300)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
