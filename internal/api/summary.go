package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/kaiye/meeting-room/internal/store"
)

func (s *Server) listSummaryTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.st.ListTemplates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) saveSummaryTemplate(w http.ResponseWriter, r *http.Request) {
	var t store.SummaryTemplate
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if id := mux.Vars(r)["id"]; id != "" {
		t.ID = id
	}
	t.Name = strings.TrimSpace(t.Name)
	t.Prompt = strings.TrimSpace(t.Prompt)
	if t.Name == "" || t.Prompt == "" {
		writeError(w, http.StatusBadRequest, "模板名称和提示词不能为空")
		return
	}
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	// Builtin flag is preserved for existing rows by UpsertTemplate's
	// conflict clause not touching it; new rows default to custom.
	if err := s.st.UpsertTemplate(r.Context(), t); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved, err := s.st.GetTemplate(r.Context(), t.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deleteSummaryTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteTemplate(r.Context(), mux.Vars(r)["id"]); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// generateSummary starts an async summary job; results arrive via the
// summary-update WS event and GET /meetings/{id}/summary.
func (s *Server) generateSummary(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var body struct {
		Template     string `json:"template"`
		CustomPrompt string `json:"customPrompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if _, err := s.st.GetTemplate(r.Context(), body.Template); err != nil {
		writeError(w, http.StatusBadRequest, "unknown template: "+body.Template)
		return
	}
	meeting, err := s.st.GetMeeting(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The transcript is still gaining segments while recording finalizes /
	// transcription runs; a summary generated now would miss the ending.
	if meeting.Status == "recording" || meeting.Status == "processing" {
		writeError(w, http.StatusConflict, "转录尚未完成(录音收尾或转录进行中),现在生成会缺失结尾内容,请稍后再试")
		return
	}
	// Queue atomically: manual requests and server-side automation may race.
	job, err := s.st.StartSummarizeJob(r.Context(), id, body.Template, body.CustomPrompt)
	if errors.Is(err, store.ErrSummaryBusy) {
		writeError(w, http.StatusConflict, "该会议的纪要正在生成中,请等待本次完成或先点取消")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Announce immediately so every open view switches to "generating" —
	// automatic first-summary runs are triggered globally, not from the
	// meeting page, which otherwise keeps showing "no summary yet".
	s.hub.Broadcast(map[string]any{
		"type": "summary-update", "meetingId": id, "status": "generating",
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) updateSummaryPrompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Template     string `json:"template"`
		CustomPrompt string `json:"customPrompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if _, err := s.st.GetTemplate(r.Context(), body.Template); err != nil {
		writeError(w, http.StatusBadRequest, "unknown template: "+body.Template)
		return
	}
	if err := s.st.UpdateSummaryPrompt(r.Context(), mux.Vars(r)["id"], body.Template, strings.TrimSpace(body.CustomPrompt)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"customPrompt": strings.TrimSpace(body.CustomPrompt)})
}

func (s *Server) getSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.st.GetSummary(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}
