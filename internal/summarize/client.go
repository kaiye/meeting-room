// Package summarize generates meeting summaries through any
// OpenAI-compatible chat completion endpoint (Ollama's /v1, OpenAI, etc.).
package summarize

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

type Client struct {
	BaseURL string
	Model   string
	APIKey  string
	http    *http.Client
}

func New(baseURL, model, apiKey string) *Client {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasSuffix(u, "/v1") {
		u += "/v1"
	}
	return &Client{BaseURL: u, Model: model, APIKey: apiKey, http: &http.Client{}}
}

// Chat runs one non-streaming completion. Local models chew on long
// transcripts slowly, so the deadline is generous.
func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"stream": false,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		msg := string(payload)
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		return "", fmt.Errorf("LLM returned HTTP %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("unexpected LLM response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
