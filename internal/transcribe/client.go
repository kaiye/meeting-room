// Package transcribe is a client for OpenAI-compatible speech-to-text
// services (POST /v1/audio/transcriptions). It is a Go port of the desktop
// app's openai_compat.rs provider, verified against MOSS-Transcribe-Diarize.
package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	// BaseURL is normalized to always end with /v1 and no trailing slash.
	BaseURL string
	Model   string
	APIKey  string
	// Speakers are voiceprint references attached to diarized requests as
	// known_speaker_names[]/known_speaker_references[]. Local services
	// accept up to 16; the official OpenAI API hard-caps at 4 (we truncate
	// accordingly at send time).
	Speakers []SpeakerRef
	// Hotwords bias decoding toward domain terms via the local-only
	// `hotwords` field (comma separated). Never sent to the official API,
	// which lacks the field. The former `prompt` channel is deliberately
	// unused: official diarize models reject it and the local MOSS route
	// treats it as a full instruction override.
	Hotwords []string
	// Debug, when set, receives one record per request with the exact
	// endpoint, fields sent (reference audio elided) and the outcome.
	Debug func(info map[string]any)
	http  *http.Client
}

// SpeakerRef pairs a speaker's display name with a reference clip encoded
// as a data URL ("data:audio/wav;base64,...", 2-10 seconds of clean speech).
type SpeakerRef struct {
	Name    string
	DataURL string
}

// Segment is one transcript segment as returned by the service. Start/End
// are nil when the service returned plain text without timestamps.
type Segment struct {
	Text    string
	Speaker string
	StartS  *float64
	EndS    *float64
}

// NormalizeBaseURL trims whitespace and trailing slashes and appends /v1
// if it is missing, so users may configure either http://host:port or
// http://host:port/v1.
func NormalizeBaseURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if !strings.HasSuffix(u, "/v1") {
		u += "/v1"
	}
	return u
}

func New(baseURL, model, apiKey string) *Client {
	return &Client{
		BaseURL: NormalizeBaseURL(baseURL),
		Model:   model,
		APIKey:  apiKey,
		// No global timeout: per-request deadlines are set from audio length.
		http: &http.Client{},
	}
}

// Healthy checks GET /v1/models with a short timeout.
func (c *Client) Healthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("transcription service unreachable at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("transcription service health check failed: HTTP %d", resp.StatusCode)
	}
	return nil
}

// diarized reports whether the configured model speaks the diarized_json
// response format (gpt-4o-transcribe-diarize style).
func (c *Client) diarized() bool {
	return strings.Contains(strings.ToLower(c.Model), "diarize")
}

// Official reports whether this client points at the real OpenAI API,
// which enforces stricter limits (4 speakers, 10s references, no hotwords).
func (c *Client) Official() bool {
	return strings.Contains(c.BaseURL, "api.openai.com")
}

func (c *Client) official() bool { return c.Official() }

func (c *Client) maxSpeakers() int {
	if c.official() {
		return 4
	}
	return 16
}

// TranscribeWAV submits one WAV payload and returns its segments.
// audioSeconds drives the request deadline: 120s fixed overhead plus 6x
// real-time, which comfortably covers MOSS on Apple Silicon (RTF ~0.56 on
// 10-minute chunks) while still failing fast on a hung service.
func (c *Client) TranscribeWAV(ctx context.Context, wav []byte, audioSeconds float64, language string) (segs []Segment, err error) {
	timeout := time.Duration(120+audioSeconds*6) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Debug record: mirrors exactly what goes on the wire (minus payloads).
	fields := map[string]any{"model": c.Model, "file": fmt.Sprintf("audio.wav (%.0fs, %d bytes)", audioSeconds, len(wav))}
	start := time.Now()
	if c.Debug != nil {
		defer func() {
			info := map[string]any{
				"url":            c.BaseURL + "/audio/transcriptions",
				"timeoutSeconds": int(timeout.Seconds()),
				"fields":         fields,
				"elapsedMs":      time.Since(start).Milliseconds(),
			}
			if err != nil {
				info["error"] = err.Error()
			} else {
				info["segments"] = len(segs)
			}
			c.Debug(info)
		}()
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(wav); err != nil {
		return nil, err
	}
	if err := mw.WriteField("model", c.Model); err != nil {
		return nil, err
	}
	if c.diarized() {
		if err := mw.WriteField("response_format", "diarized_json"); err != nil {
			return nil, err
		}
		if err := mw.WriteField("chunking_strategy", "auto"); err != nil {
			return nil, err
		}
		fields["response_format"] = "diarized_json"
		fields["chunking_strategy"] = "auto"
		names := []string{}
		for i, ref := range c.Speakers {
			if i >= c.maxSpeakers() {
				break
			}
			if err := mw.WriteField("known_speaker_names[]", ref.Name); err != nil {
				return nil, err
			}
			if err := mw.WriteField("known_speaker_references[]", ref.DataURL); err != nil {
				return nil, err
			}
			names = append(names, ref.Name)
		}
		if len(names) > 0 {
			fields["known_speaker_names[]"] = names
			fields["known_speaker_references[]"] = fmt.Sprintf("%d 段参考音频(data URL 略)", len(names))
		}
	}
	if len(c.Hotwords) > 0 && !c.official() {
		hw := strings.Join(c.Hotwords, ",")
		if err := mw.WriteField("hotwords", hw); err != nil {
			return nil, err
		}
		fields["hotwords"] = hw
	}
	if lang := strings.TrimSpace(language); lang != "" && lang != "auto" && lang != "auto-translate" {
		if err := mw.WriteField("language", lang); err != nil {
			return nil, err
		}
		fields["language"] = lang
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/audio/transcriptions", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c.auth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("transcription service returned HTTP %d: %s", resp.StatusCode, truncate(string(payload), 500))
	}
	return ParseResponse(payload)
}

// ParseResponse handles both diarized_json ({segments: [...]}) and plain
// json ({text: "..."}) response shapes.
func ParseResponse(payload []byte) ([]Segment, error) {
	var raw struct {
		Text     string `json:"text"`
		Segments []struct {
			Start   *float64 `json:"start"`
			End     *float64 `json:"end"`
			Speaker string   `json:"speaker"`
			Text    string   `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("unexpected transcription response: %w", err)
	}
	if len(raw.Segments) == 0 {
		text := strings.TrimSpace(raw.Text)
		if text == "" {
			return nil, nil
		}
		return []Segment{{Text: text}}, nil
	}
	segs := make([]Segment, 0, len(raw.Segments))
	for _, s := range raw.Segments {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		segs = append(segs, Segment{
			Text:    text,
			Speaker: strings.TrimSpace(s.Speaker),
			StartS:  s.Start,
			EndS:    s.End,
		})
	}
	return segs, nil
}

func (c *Client) auth(req *http.Request) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
