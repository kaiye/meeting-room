// Package api exposes the REST + WebSocket surface: one HTTP endpoint per
// operation, with server-pushed events delivered over /api/v1/ws.
package api

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gorilla/mux"

	"github.com/kaiye/meeting-room/internal/audio"
	"github.com/kaiye/meeting-room/internal/config"
	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/transcribe"
	"github.com/kaiye/meeting-room/internal/ws"
)

// Minimal fallback UI, embedded so the binary works even before anyone has
// built frontend/dist. The real UI is the React app served from WebDir.
//
//go:embed webui/index.html
var indexHTML []byte

type Server struct {
	cfg config.Config
	st  *store.Store
	hub *ws.Hub

	// Meetings this process is recording or finalizing. The "recording" status
	// in the DB cannot distinguish "a live socket is feeding it" from "the
	// process that owned it died", and nothing stops a second instance from
	// opening the same data dir — so the manual stop below consults this
	// map, which only ever answers for the process that owns the socket.
	liveMu     sync.Mutex
	liveActive map[string]bool
}

func NewRouter(cfg config.Config, st *store.Store, hub *ws.Hub) *mux.Router {
	s := &Server{cfg: cfg, st: st, hub: hub, liveActive: map[string]bool{}}
	r := mux.NewRouter()
	r.HandleFunc("/healthz", s.health).Methods(http.MethodGet)

	v1 := r.PathPrefix("/api/v1").Subrouter()
	v1.Use(s.authMiddleware)
	v1.HandleFunc("/meetings", s.listMeetings).Methods(http.MethodGet)
	v1.HandleFunc("/meetings/import", s.importMeeting).Methods(http.MethodPost)
	v1.HandleFunc("/meetings/{id}", s.getMeeting).Methods(http.MethodGet)
	v1.HandleFunc("/meetings/{id}", s.renameMeeting).Methods(http.MethodPatch)
	v1.HandleFunc("/meetings/{id}", s.deleteMeeting).Methods(http.MethodDelete)
	v1.HandleFunc("/meetings/{id}/transcript", s.getTranscript).Methods(http.MethodGet)
	v1.HandleFunc("/meetings/{id}/audio", s.serveMeetingAudio).Methods(http.MethodGet)
	v1.HandleFunc("/meetings/{id}/audio/truncate", s.truncateMeetingAudio).Methods(http.MethodPost)
	v1.HandleFunc("/meetings/{id}/transcribe-debug", s.listTranscribeDebug).Methods(http.MethodGet)
	v1.HandleFunc("/meetings/{id}/summary-debug", s.listSummaryDebug).Methods(http.MethodGet)
	v1.HandleFunc("/meetings/{id}/summarize/cancel", s.cancelSummarize).Methods(http.MethodPost)
	v1.HandleFunc("/meetings/{id}/recording/stop", s.stopStuckRecording).Methods(http.MethodPost)
	v1.HandleFunc("/meetings/{id}/retranscribe", s.retranscribe).Methods(http.MethodPost)
	v1.HandleFunc("/meetings/{id}/summarize", s.generateSummary).Methods(http.MethodPost)
	v1.HandleFunc("/meetings/{id}/summary", s.getSummary).Methods(http.MethodGet)
	v1.HandleFunc("/meetings/{id}/summary-prompt", s.updateSummaryPrompt).Methods(http.MethodPut)
	v1.HandleFunc("/meetings/{id}/comments", s.listComments).Methods(http.MethodGet)
	v1.HandleFunc("/meetings/{id}/comments", s.addComment).Methods(http.MethodPost)
	v1.HandleFunc("/meetings/{id}/comments/{commentId}", s.updateComment).Methods(http.MethodPut)
	v1.HandleFunc("/meetings/{id}/comments/{commentId}", s.deleteComment).Methods(http.MethodDelete)
	v1.HandleFunc("/meetings/{id}/participants", s.setParticipants).Methods(http.MethodPut)
	v1.HandleFunc("/meetings/{id}/context", s.setMeetingContext).Methods(http.MethodPut)
	v1.HandleFunc("/meetings/{id}/transcribe/cancel", s.cancelTranscribe).Methods(http.MethodPost)
	v1.HandleFunc("/meetings/{id}/autoname", s.autoName).Methods(http.MethodPost)
	v1.HandleFunc("/summary-templates", s.listSummaryTemplates).Methods(http.MethodGet)
	v1.HandleFunc("/summary-templates", s.saveSummaryTemplate).Methods(http.MethodPost)
	v1.HandleFunc("/summary-templates/{id}", s.saveSummaryTemplate).Methods(http.MethodPut)
	v1.HandleFunc("/summary-templates/{id}", s.deleteSummaryTemplate).Methods(http.MethodDelete)
	v1.HandleFunc("/speakers", s.listSpeakers).Methods(http.MethodGet)
	v1.HandleFunc("/speakers", s.createMember).Methods(http.MethodPost)
	v1.HandleFunc("/speakers/enroll", s.enrollSpeaker).Methods(http.MethodPost)
	v1.HandleFunc("/speakers/ensure", s.ensureMembers).Methods(http.MethodPost)
	v1.HandleFunc("/speakers/merge", s.mergeMembers).Methods(http.MethodPost)
	v1.HandleFunc("/speakers/clips/{clipId}/audio", s.serveClipAudio).Methods(http.MethodGet)
	v1.HandleFunc("/speakers/clips/{clipId}", s.deleteSpeakerClip).Methods(http.MethodDelete)
	v1.HandleFunc("/speakers/{id}/clips", s.listSpeakerClips).Methods(http.MethodGet)
	v1.HandleFunc("/speakers/{id}/sample/audio", s.serveSpeakerSample).Methods(http.MethodGet)
	v1.HandleFunc("/speakers/{id}", s.updateSpeaker).Methods(http.MethodPut)
	v1.HandleFunc("/speakers/{id}", s.deleteSpeaker).Methods(http.MethodDelete)
	v1.HandleFunc("/settings/models", s.listRemoteModels).Methods(http.MethodPost)
	v1.HandleFunc("/settings", s.getSettings).Methods(http.MethodGet)
	v1.HandleFunc("/settings/transcribe", s.putTranscribeSettings).Methods(http.MethodPut)
	v1.HandleFunc("/settings/transcribe/test", s.testTranscribe).Methods(http.MethodPost)
	v1.HandleFunc("/settings/summary", s.putSummarySettings).Methods(http.MethodPut)
	v1.HandleFunc("/settings/context", s.putContextSettings).Methods(http.MethodPut)
	v1.HandleFunc("/settings/calendar", s.putCalendarSettings).Methods(http.MethodPut)
	v1.HandleFunc("/calendar/today", s.calendarToday).Methods(http.MethodGet)
	v1.HandleFunc("/ws", hub.HandleWS).Methods(http.MethodGet)
	v1.HandleFunc("/live", s.handleLive).Methods(http.MethodGet)

	// Frontend: serve the built SPA when present (client-side routes fall
	// back to index.html); otherwise the embedded dev-preview page.
	if fi, err := os.Stat(filepath.Join(cfg.WebDir, "index.html")); err == nil && !fi.IsDir() {
		webDir := cfg.WebDir
		r.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			path := filepath.Join(webDir, filepath.Clean("/"+req.URL.Path))
			if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
				http.ServeFile(w, req, path)
				return
			}
			http.ServeFile(w, req, filepath.Join(webDir, "index.html"))
		}).Methods(http.MethodGet)
		log.Printf("serving frontend from %s", webDir)
	} else {
		r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(indexHTML)
		}).Methods(http.MethodGet)
	}
	return r
}

// authMiddleware enforces the shared team token. Browser WebSocket clients
// cannot set headers, so ?token= is accepted there as well.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != s.cfg.APIToken {
			writeError(w, http.StatusUnauthorized, "invalid or missing API token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) listMeetings(w http.ResponseWriter, r *http.Request) {
	meetings, err := s.st.ListMeetings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, meetings)
}

// importMeeting accepts a multipart upload (file, title?, language?),
// stores the original file under DataDir, and enqueues an import job.
// The response is immediate (202); progress arrives via /ws.
func (s *Server) importMeeting(w http.ResponseWriter, r *http.Request) {
	// Files beyond the memory threshold spool to disk automatically.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'file' field")
		return
	}
	defer file.Close()

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	}
	language := strings.TrimSpace(r.FormValue("language"))
	if language == "" {
		// Fall back to the default language from transcribe settings.
		if ts, err := s.st.GetTranscribeSettings(r.Context()); err == nil && ts.Language != "" {
			language = ts.Language
		} else {
			language = "zh"
		}
	}

	meeting, err := s.st.CreateMeeting(r.Context(), title, language, "", "import")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	dir := filepath.Join(s.cfg.DataDir, "meetings", meeting.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = ".bin"
	}
	dst := filepath.Join(dir, "original"+ext)
	out, err := os.Create(dst)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.Close()

	if err := s.st.SetMeetingAudioPath(r.Context(), meeting.ID, dst); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	job, err := s.st.CreateJob(r.Context(), meeting.ID, "import")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("api: import %q (%d bytes) -> meeting %s job %s", header.Filename, header.Size, meeting.ID, job.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"meeting": meeting, "job": job})
}

func (s *Server) getMeeting(w http.ResponseWriter, r *http.Request) {
	meeting, err := s.st.GetMeeting(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeStoreError(w, err)
		return
	}
	resp := map[string]any{"meeting": meeting}
	if job, err := s.st.GetLatestJob(r.Context(), meeting.ID); err == nil {
		resp["job"] = job
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) renameMeeting(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if err := s.st.RenameMeeting(r.Context(), mux.Vars(r)["id"], strings.TrimSpace(body.Title)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) deleteMeeting(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	meeting, err := s.st.GetMeeting(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.st.DeleteMeeting(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	// Best-effort file cleanup; DB row is the source of truth.
	if meeting.AudioPath != nil {
		os.RemoveAll(filepath.Dir(*meeting.AudioPath))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// serveMeetingAudio streams the meeting recording for seek-to-segment
// playback. ServeFile brings HTTP Range support for free, so <audio>
// seeking works on long recordings. During a live meeting (no finalized
// WAV yet) it wraps the raw PCM snapshot on the fly.
func (s *Server) serveMeetingAudio(w http.ResponseWriter, r *http.Request) {
	meeting, err := s.st.GetMeeting(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if meeting.AudioPath != nil {
		// mime.TypeByExtension may miss .m4a; set it so <audio> seeking works.
		if strings.HasSuffix(*meeting.AudioPath, ".m4a") {
			w.Header().Set("Content-Type", "audio/mp4")
		}
		http.ServeFile(w, r, *meeting.AudioPath)
		return
	}
	pcmPath := filepath.Join(s.cfg.DataDir, "meetings", meeting.ID, "live.pcm")
	pcm, err := os.ReadFile(pcmPath)
	if err != nil || len(pcm) == 0 {
		writeError(w, http.StatusNotFound, "该会议还没有音频")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Write(transcribe.EncodeWAV(pcm, transcribe.SampleRate))
}

// truncateMeetingAudio cuts the recording at the given position, dropping
// everything after it (audio + transcript segments) — the fix for "forgot
// to stop the recording". The previous audio survives as *.bak.* on disk.
func (s *Server) truncateMeetingAudio(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seconds float64 `json:"seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Seconds < 1 {
		writeError(w, http.StatusBadRequest, "截断位置无效(至少保留 1 秒)")
		return
	}
	meeting, err := s.st.GetMeeting(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if meeting.AudioPath == nil {
		writeError(w, http.StatusConflict, "该会议没有音频")
		return
	}
	if meeting.Status == "recording" || meeting.Status == "processing" {
		writeError(w, http.StatusConflict, "录音或转录进行中,无法截断")
		return
	}
	if meeting.DurationSeconds != nil && body.Seconds >= *meeting.DurationSeconds {
		writeError(w, http.StatusBadRequest, "截断位置超出音频长度")
		return
	}
	if err := audio.TruncateAudio(r.Context(), *meeting.AudioPath, body.Seconds); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.st.SetMeetingDuration(r.Context(), meeting.ID, body.Seconds); err != nil {
		writeStoreError(w, err)
		return
	}
	dropped, err := s.st.DeleteSegmentsFrom(r.Context(), meeting.ID, int64(body.Seconds*1000))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	log.Printf("api: truncated meeting %s audio to %.0fs, dropped %d segments", meeting.ID, body.Seconds, dropped)
	s.hub.Broadcast(map[string]any{"type": "meeting-updated", "meetingId": meeting.ID})
	writeJSON(w, http.StatusOK, map[string]any{"durationSeconds": body.Seconds, "droppedSegments": dropped})
}

func (s *Server) listTranscribeDebug(w http.ResponseWriter, r *http.Request) {
	ds, err := s.st.ListTranscribeDebug(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

func (s *Server) listSummaryDebug(w http.ResponseWriter, r *http.Request) {
	ds, err := s.st.ListSummaryDebug(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

func (s *Server) cancelSummarize(w http.ResponseWriter, r *http.Request) {
	found, err := s.st.RequestSummarizeCancel(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "没有进行中的总结任务")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

func (s *Server) getTranscript(w http.ResponseWriter, r *http.Request) {
	segs, err := s.st.GetSegments(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, segs)
}

func (s *Server) retranscribe(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	meeting, err := s.st.GetMeeting(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if meeting.AudioPath == nil {
		writeError(w, http.StatusConflict, "meeting has no stored audio to retranscribe")
		return
	}
	job, err := s.st.CreateJob(r.Context(), id, "retranscribe")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, fmt.Sprintf("%v", err))
}
