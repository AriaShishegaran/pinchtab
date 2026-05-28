package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiModel solves grid challenges via a direct vision API call. This is the
// optional path for operators who have an LLM API key; the CLI passthrough is
// the default and needs no key.
type apiModel struct {
	provider string // "anthropic" | "openai"
	model    string
	apiKey   string
	baseURL  string
	http     *http.Client
}

func newAPIModel(provider string, cfg Config) (*apiModel, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("vision api: provider %q selected but no API key configured", provider)
	}
	model := strings.TrimSpace(cfg.Model)
	base := strings.TrimSpace(cfg.BaseURL)
	switch provider {
	case "anthropic":
		if model == "" {
			model = "claude-sonnet-4-5"
		}
		if base == "" {
			base = "https://api.anthropic.com"
		}
	case "openai":
		if model == "" {
			model = "gpt-4o"
		}
		if base == "" {
			base = "https://api.openai.com"
		}
	default:
		return nil, fmt.Errorf("vision api: unsupported provider %q", provider)
	}
	return &apiModel{
		provider: provider,
		model:    model,
		apiKey:   cfg.APIKey,
		baseURL:  strings.TrimRight(base, "/"),
		http:     &http.Client{Timeout: 90 * time.Second},
	}, nil
}

func (m *apiModel) Name() string { return "api:" + m.provider + ":" + m.model }

func (m *apiModel) SolveGrid(ctx context.Context, ch GridChallenge) (GridSolution, error) {
	if len(ch.Image) == 0 {
		return GridSolution{}, fmt.Errorf("vision api: empty image")
	}
	b64 := base64.StdEncoding.EncodeToString(ch.Image)
	instruction := gridInstruction(ch)

	var text string
	var err error
	switch m.provider {
	case "anthropic":
		text, err = m.callAnthropic(ctx, b64, instruction)
	case "openai":
		text, err = m.callOpenAI(ctx, b64, instruction)
	default:
		return GridSolution{}, fmt.Errorf("vision api: unsupported provider %q", m.provider)
	}
	if err != nil {
		return GridSolution{}, err
	}
	return parseGridSolution(text)
}

func (m *apiModel) callAnthropic(ctx context.Context, imgB64, instruction string) (string, error) {
	body := map[string]any{
		"model":      m.model,
		"max_tokens": 512,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": "image/png", "data": imgB64,
				}},
				{"type": "text", "text": instruction},
			},
		}},
	}
	raw, err := m.do(ctx, m.baseURL+"/v1/messages", body, map[string]string{
		"x-api-key":         m.apiKey,
		"anthropic-version": "2023-06-01",
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("vision anthropic: decode: %w", err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("vision anthropic: %s", resp.Error.Message)
	}
	var b strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), nil
}

func (m *apiModel) callOpenAI(ctx context.Context, imgB64, instruction string) (string, error) {
	body := map[string]any{
		"model":      m.model,
		"max_tokens": 512,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": instruction},
				{"type": "image_url", "image_url": map[string]any{
					"url": "data:image/png;base64," + imgB64,
				}},
			},
		}},
	}
	raw, err := m.do(ctx, m.baseURL+"/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer " + m.apiKey,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("vision openai: decode: %w", err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("vision openai: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("vision openai: empty choices")
	}
	return resp.Choices[0].Message.Content, nil
}

func (m *apiModel) do(ctx context.Context, url string, body map[string]any, headers map[string]string) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vision api: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("vision api: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vision api: do: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("vision api: read: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("vision api: http %d: %s", resp.StatusCode, truncate(string(raw), 240))
	}
	return raw, nil
}
