package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaiye/meeting-room/internal/calendar"
	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/transcribe"
)

// getSettings returns the *effective* configuration the worker will use:
// UI-saved values, with .env deployment defaults filled into empty fields.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	ts, err := s.st.GetTranscribeSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ss, err := s.st.GetSummarySettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cs, err := s.st.GetContextSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cal, err := s.st.GetCalendarSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"transcribe": s.effectiveTranscribe(ts),
		"summary":    ss,
		"context":    cs,
		"calendar":   cal,
	})
}

func (s *Server) putCalendarSettings(w http.ResponseWriter, r *http.Request) {
	var cs store.CalendarSettings
	if err := json.NewDecoder(r.Body).Decode(&cs); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := s.st.SaveCalendarSettings(r.Context(), cs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// calendarToday proxies today's events from the subscribed ICS feed.
func (s *Server) calendarToday(w http.ResponseWriter, r *http.Request) {
	cs, err := s.st.GetCalendarSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if strings.TrimSpace(cs.ICSURL) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "events": []any{}})
		return
	}
	events, err := calendar.Today(r.Context(), strings.TrimSpace(cs.ICSURL), r.URL.Query().Get("force") == "1")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "error": err.Error(), "events": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "events": events})
}

func (s *Server) putContextSettings(w http.ResponseWriter, r *http.Request) {
	var cs store.ContextSettings
	if err := json.NewDecoder(r.Body).Decode(&cs); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := s.st.SaveContextSettings(r.Context(), cs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) putTranscribeSettings(w http.ResponseWriter, r *http.Request) {
	var ts store.TranscribeSettings
	if err := json.NewDecoder(r.Body).Decode(&ts); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := s.st.SaveTranscribeSettings(r.Context(), ts); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.effectiveTranscribe(ts))
}

func (s *Server) putSummarySettings(w http.ResponseWriter, r *http.Request) {
	var ss store.SummarySettings
	if err := json.NewDecoder(r.Body).Decode(&ss); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := s.st.SaveSummarySettings(r.Context(), ss); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ss)
}

// testTranscribe health-checks a transcription service. With a JSON body it
// tests the posted config (so the UI can validate before saving); with an
// empty body it tests the currently effective config.
func (s *Server) testTranscribe(w http.ResponseWriter, r *http.Request) {
	var ts store.TranscribeSettings
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&ts); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}
	if ts.BaseURL == "" {
		stored, err := s.st.GetTranscribeSettings(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ts = s.effectiveTranscribe(stored)
	}
	if ts.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "转录服务地址为空")
		return
	}
	client := transcribe.New(ts.BaseURL, ts.Model, ts.APIKey)
	if err := client.Healthy(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "baseUrl": client.BaseURL})
}

// listRemoteModels proxies GET {baseUrl}/models so the settings UI can
// offer a picker instead of free-text model names (the browser can't call
// the service directly across origins).
func (s *Server) listRemoteModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "baseUrl is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		transcribe.NormalizeBaseURL(body.BaseURL)+"/models", nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+body.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "服务不可达: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "HTTP " + resp.Status})
		return
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil || len(parsed.Data) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "无法解析模型列表"})
		return
	}
	models := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		models = append(models, m.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": models})
}

func (s *Server) effectiveTranscribe(ts store.TranscribeSettings) store.TranscribeSettings {
	if ts.BaseURL == "" {
		ts.BaseURL = s.cfg.TranscribeBaseURL
	}
	if ts.Model == "" {
		ts.Model = s.cfg.TranscribeModel
	}
	if ts.APIKey == "" {
		ts.APIKey = s.cfg.TranscribeAPIKey
	}
	if ts.ChunkSeconds <= 0 {
		ts.ChunkSeconds = s.cfg.ChunkSeconds
	}
	if ts.LiveChunkSeconds <= 0 {
		ts.LiveChunkSeconds = s.cfg.LiveChunkSeconds
	}
	if ts.Language == "" {
		ts.Language = "zh"
	}
	// LiveModel intentionally has no default: empty = follow Model.
	return ts
}
