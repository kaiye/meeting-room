package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"

	"github.com/kaiye/meeting-room/internal/jobs"
)

// Comments turn "chat with the summarizer" into structured feedback: users
// select transcript/summary text, annotate it, and regeneration folds every
// comment into the prompt.

// commentAuthor signs revisions. Until sign-in exists this is whoever runs
// the server: set MEETING_ROOM_AUTHOR to have your own name on them.
var commentAuthor = func() string {
	if v := strings.TrimSpace(os.Getenv("MEETING_ROOM_AUTHOR")); v != "" {
		return v
	}
	return "我"
}()

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	cs, err := s.st.ListComments(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) addComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
		Kind   string `json:"kind"`
		Quote  string `json:"quote"`
		Body   string `json:"body"`
		Anchor string `json:"anchor"`
		RevisionGroupID string `json:"revisionGroupId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Body) == "" {
		writeError(w, http.StatusBadRequest, "内容不能为空")
		return
	}
	if body.Target != "summary" && body.Target != "general" {
		body.Target = "transcript"
	}
	if body.Target == "general" {
		body.Kind = "note"
	} else if body.Kind != "comment" && body.Kind != "delete" {
		body.Kind = "rewrite"
	}
	if len(body.Anchor) > 64 {
		body.Anchor = ""
	}
	body.RevisionGroupID = strings.TrimSpace(body.RevisionGroupID)
	if len(body.RevisionGroupID) > 64 { body.RevisionGroupID = "" }
	c, err := s.st.AddComment(r.Context(), mux.Vars(r)["id"], body.Target, body.Kind,
		strings.TrimSpace(body.Quote), strings.TrimSpace(body.Body), commentAuthor, body.Anchor, body.RevisionGroupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) updateComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Body) == "" {
		writeError(w, http.StatusBadRequest, "批注内容不能为空")
		return
	}
	vars := mux.Vars(r)
	c, err := s.st.UpdateComment(r.Context(), vars["id"], vars["commentId"], strings.TrimSpace(body.Body))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	if err := s.st.DeleteComment(r.Context(), vars["id"], vars["commentId"]); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// setParticipants stores this meeting's roster: which enrolled voiceprints
// ride along on its transcription requests.
func (s *Server) setParticipants(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Participants []string `json:"participants"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := s.st.SetMeetingParticipants(r.Context(), mux.Vars(r)["id"], body.Participants); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"participants": body.Participants})
}

// setMeetingContext stores per-meeting background info (calendar invite,
// agenda…) used by summary generation and AI titling.
func (s *Server) setMeetingContext(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Context string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := s.st.SetMeetingContext(r.Context(), mux.Vars(r)["id"], strings.TrimSpace(body.Context)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// cancelTranscribe flags the meeting's pending/running transcription job;
// the worker aborts between chunks (a few seconds at most).
func (s *Server) cancelTranscribe(w http.ResponseWriter, r *http.Request) {
	found, err := s.st.RequestTranscribeCancel(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "没有进行中的转录任务")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

// autoName is the explicit "AI rename" button: force-applies an LLM title
// even over a manual rename, and puts the title back under auto management.
func (s *Server) autoName(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	title, _, err := jobs.AutoTitle(r.Context(), s.st, id, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.hub.Broadcast(map[string]any{"type": "meeting-updated", "meetingId": id, "title": title})
	writeJSON(w, http.StatusOK, map[string]string{"title": title})
}
