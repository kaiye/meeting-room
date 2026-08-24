package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// TranscribeSettings selects the speech-to-text service. It may point at a
// local/LAN OpenAI-compatible deployment (MOSS, whisper server) or at the
// real OpenAI API (https://api.openai.com/v1) — same protocol either way.
type TranscribeSettings struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	APIKey  string `json:"apiKey"`
	// Live* configure a SEPARATE service for live rolling transcription
	// (subtitles) — a fast model/deployment for realtime, while imports
	// and retranscribes use the accurate one above. Each empty field
	// falls back to its batch counterpart.
	LiveBaseURL string `json:"liveBaseUrl"`
	LiveModel   string `json:"liveModel"`
	LiveAPIKey  string `json:"liveApiKey"`
	// Language is the default transcription language (zh/en/auto); a
	// per-import override can still be chosen in the import dialog.
	Language string `json:"language"`
	// ChunkSeconds / LiveChunkSeconds tune the request cadence per service:
	// MOSS-class needs 5-10 min chunks (small chunks wreck its RTF), while
	// funasr-class works best at 30-60s. 0 means "use the env default".
	ChunkSeconds     int `json:"chunkSeconds"`
	LiveChunkSeconds int `json:"liveChunkSeconds"`
}

// SummarySettings selects the LLM used for meeting summaries
// (Ollama local, OpenAI-compatible endpoint, etc.).
type SummarySettings struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	APIKey  string `json:"apiKey"`
}

// ContextSettings is team-wide background knowledge: project context and a
// domain glossary. Both feed summary generation; the glossary additionally
// rides along as the transcription `prompt` field (services that support
// term priming use it, others ignore it).
type ContextSettings struct {
	Background string `json:"background"`
	Glossary   string `json:"glossary"`
}

// CalendarSettings holds the subscribed ICS feed (Google Calendar's
// "secret address in iCal format" or any other provider's ICS URL).
type CalendarSettings struct {
	ICSURL string `json:"icsUrl"`
}

const (
	settingTranscribe = "transcribe"
	settingSummary    = "summary"
	settingContext    = "context"
	settingCalendar   = "calendar"
)

func (s *Store) GetCalendarSettings(ctx context.Context) (CalendarSettings, error) {
	var cs CalendarSettings
	err := s.getSettingJSON(ctx, settingCalendar, &cs)
	return cs, err
}

func (s *Store) SaveCalendarSettings(ctx context.Context, cs CalendarSettings) error {
	return s.setSettingJSON(ctx, settingCalendar, cs)
}

func (s *Store) GetContextSettings(ctx context.Context) (ContextSettings, error) {
	var cs ContextSettings
	err := s.getSettingJSON(ctx, settingContext, &cs)
	return cs, err
}

func (s *Store) SaveContextSettings(ctx context.Context, cs ContextSettings) error {
	return s.setSettingJSON(ctx, settingContext, cs)
}

func (s *Store) GetTranscribeSettings(ctx context.Context) (TranscribeSettings, error) {
	var ts TranscribeSettings
	err := s.getSettingJSON(ctx, settingTranscribe, &ts)
	return ts, err
}

func (s *Store) SaveTranscribeSettings(ctx context.Context, ts TranscribeSettings) error {
	return s.setSettingJSON(ctx, settingTranscribe, ts)
}

func (s *Store) GetSummarySettings(ctx context.Context) (SummarySettings, error) {
	var ss SummarySettings
	err := s.getSettingJSON(ctx, settingSummary, &ss)
	return ss, err
}

func (s *Store) SaveSummarySettings(ctx context.Context, ss SummarySettings) error {
	return s.setSettingJSON(ctx, settingSummary, ss)
}

// getSettingJSON leaves v at its zero value when the key has never been set.
func (s *Store) getSettingJSON(ctx context.Context, key string, v any) error {
	var raw string
	err := s.db.GetContext(ctx, &raw, `SELECT value FROM settings WHERE key = $1`, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

func (s *Store) setSettingJSON(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = datetime('now','subsec')`,
		key, string(data))
	return err
}
