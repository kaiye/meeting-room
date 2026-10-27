package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/kaiye/meeting-room/internal/audio"
	"github.com/kaiye/meeting-room/internal/jobs"
	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/transcribe"
)

// Live recording protocol on GET /api/v1/live (WebSocket):
//
//	client -> text   {"type":"start","title":"...","language":"zh"}
//	server -> text   {"type":"started","meetingId":"..."}
//	client -> binary raw s16le mono 16kHz PCM frames (any framing)
//	client -> text   {"type":"stop"}   (or just disconnects)
//	server -> text   {"type":"stopped","meetingId":"..."}
//
// While recording, every liveChunkSeconds of accumulated audio gets one
// rolling transcription pass; the cut prefers a >=300ms pause (energy VAD)
// near the window boundary and only falls back to a hard cut after 15
// extra seconds of continuous speech. Segments are appended to the DB and
// broadcast as {"type":"live-transcript"} on /api/v1/ws. On stop only the
// tail that no rolling pass covered yet is transcribed — the rolling
// transcript IS the transcript; cross-window speaker naming comes from
// enrolled voiceprints (known_speaker_references), reloaded on every pass
// so mid-meeting enrollment takes effect on the next chunk.

var liveUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 4 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type liveStartMsg struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Language string `json:"language"`
	// Participants picked before recording: whose voiceprints ride along.
	// Empty means no voiceprints — the service returns raw letter labels.
	Participants []string `json:"participants"`
	// Context is optional background info (calendar invite, agenda…).
	Context string `json:"context"`
	// MeetingID resumes recording into an existing meeting (appending to
	// its audio) instead of creating a new one — the fix for accidental
	// stops.
	MeetingID string `json:"meetingId"`
	// CalendarUID links this recording to the calendar occurrence it was
	// started from.
	CalendarUID string `json:"calendarUid"`
}

const liveMaxExtraSeconds = 15 // longest we wait for a pause past the target window

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	conn, err := liveUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	// Recording sessions outlive the HTTP request context semantics we
	// want; use an independent context for persistence operations.
	ctx := context.Background()

	var start liveStartMsg
	if mt, data, err := conn.ReadMessage(); err != nil || mt != websocket.TextMessage ||
		json.Unmarshal(data, &start) != nil || start.Type != "start" {
		conn.WriteJSON(map[string]string{"type": "error", "error": "expected start message"})
		return
	}
	title := start.Title
	if title == "" {
		title = "实时会议 " + time.Now().Format("01-02 15:04")
	}
	// Client may omit language; the transcribe settings hold the default.
	language := start.Language
	if language == "" {
		if ts, err := s.st.GetTranscribeSettings(ctx); err == nil && ts.Language != "" {
			language = ts.Language
		} else {
			language = "zh"
		}
	}

	var meeting *store.Meeting
	resume := false
	if start.MeetingID != "" {
		m, err := s.st.GetMeeting(ctx, start.MeetingID)
		if err != nil {
			conn.WriteJSON(map[string]string{"type": "error", "error": "会议不存在"})
			return
		}
		if m.Status == "recording" || m.Status == "processing" {
			conn.WriteJSON(map[string]string{"type": "error", "error": "该会议正在录制或转录中,无法续录"})
			return
		}
		meeting = m
		resume = true
		if m.Language != nil && *m.Language != "" {
			language = *m.Language
		}
	} else {
		m, err := s.st.CreateMeeting(ctx, title, language, "", "live")
		if err != nil {
			conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
			return
		}
		meeting = m
	}
	// Claim before publishing "recording" or touching the PCM file. Recovery
	// and another resume must not race with session setup or finalization.
	if !s.claimLive(meeting.ID) {
		conn.WriteJSON(map[string]string{"type": "error", "error": "该会议正在录制或收尾中"})
		return
	}
	handedOff := false
	defer func() {
		if !handedOff {
			s.markLive(meeting.ID, false)
		}
	}()
	if resume {
		m, err := s.st.GetMeeting(ctx, meeting.ID)
		if err != nil || m.Status == "recording" || m.Status == "processing" {
			conn.WriteJSON(map[string]string{"type": "error", "error": "会议状态已改变,无法续录"})
			return
		}
		meeting = m
	}
	if err := s.st.SetMeetingStatus(ctx, meeting.ID, "recording"); err != nil {
		log.Printf("live: set status: %v", err)
		conn.WriteJSON(map[string]string{"type": "error", "error": "无法开始录制"})
		return
	}
	if len(start.Participants) > 0 {
		if err := s.st.SetMeetingParticipants(ctx, meeting.ID, start.Participants); err != nil {
			log.Printf("live: set participants: %v", err)
		}
	}
	if strings.TrimSpace(start.CalendarUID) != "" {
		if err := s.st.SetMeetingCalendarUID(ctx, meeting.ID, strings.TrimSpace(start.CalendarUID)); err != nil {
			log.Printf("live: set calendar uid: %v", err)
		}
	}
	if strings.TrimSpace(start.Context) != "" {
		if err := s.st.SetMeetingContext(ctx, meeting.ID, strings.TrimSpace(start.Context)); err != nil {
			log.Printf("live: set context: %v", err)
		}
	}
	dir := filepath.Join(s.cfg.DataDir, "meetings", meeting.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
		return
	}
	pcmPath := filepath.Join(dir, "live.pcm")
	pcmFile, err := os.Create(pcmPath)
	if err != nil {
		conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
		return
	}
	defer pcmFile.Close()
	// Resume: seed the PCM buffer with the existing recording so new audio
	// appends seamlessly — timestamps continue after the old duration and
	// the already-transcribed span is never re-rolled.
	var resumeBytes int64
	resumeWindow := 0
	if resume {
		if meeting.AudioPath != nil {
			pcm, _, err := audio.NormalizeToPCM16k(ctx, *meeting.AudioPath)
			if err != nil {
				conn.WriteJSON(map[string]string{"type": "error", "error": "读取原录音失败: " + err.Error()})
				pcmFile.Close()
				s.st.SetMeetingStatus(ctx, meeting.ID, "completed")
				return
			}
			if _, err := pcmFile.Write(pcm); err != nil {
				log.Printf("live: resume seed write: %v", err)
			}
			resumeBytes = int64(len(pcm))
		}
		if maxWin, err := s.st.MaxSegmentWindow(ctx, meeting.ID); err == nil {
			resumeWindow = maxWin + 1
		}
	}
	// Separate read handle for the silence scan over the growing file.
	pcmRead, err := os.Open(pcmPath)
	if err != nil {
		conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
		return
	}
	defer pcmRead.Close()

	// The window size is fixed at start; the voiceprint roster is not.
	liveChunkSeconds := s.cfg.LiveChunkSeconds
	if rt, err := jobs.LoadRuntime(ctx, s.cfg, s.st, nil); err == nil {
		liveChunkSeconds = rt.LiveChunkSeconds
	}

	conn.WriteJSON(map[string]string{"type": "started", "meetingId": meeting.ID})
	log.Printf("live: recording started, meeting %s (%q), window %ds, resume=%v(+%ds)",
		meeting.ID, meeting.Title, liveChunkSeconds, resume, resumeBytes/32000)

	bytesPerSec := int64(transcribe.SampleRate * transcribe.BytesPerSample)
	chunkBytes := int64(liveChunkSeconds) * bytesPerSec
	maxBytes := chunkBytes + liveMaxExtraSeconds*bytesPerSec
	lookbackBytes := min(10*bytesPerSec, chunkBytes/2)

	pcmBytes, rolledBytes := resumeBytes, resumeBytes
	windowIdx := resumeWindow // serial per meeting: rollingBusy guarantees ordering
	rollingBusy := make(chan struct{}, 1)

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("live: connection closed (%v), finalizing meeting %s", err, meeting.ID)
			break
		}
		switch mt {
		case websocket.BinaryMessage:
			if _, err := pcmFile.Write(data); err != nil {
				log.Printf("live: write pcm: %v", err)
			}
			pcmBytes += int64(len(data))
			// Fire a rolling pass when a full window has accumulated and no
			// pass is in flight (serial: single GPU inference lock). The cut
			// lands on a pause when one exists; with continuous speech we
			// wait up to liveMaxExtraSeconds before cutting mid-sentence.
			if pcmBytes-rolledBytes >= chunkBytes {
				select {
				case rollingBusy <- struct{}{}:
					cut := liveCutPoint(pcmRead, rolledBytes, pcmBytes, chunkBytes, maxBytes, lookbackBytes)
					if cut <= rolledBytes {
						<-rollingBusy // no pause yet; keep accumulating
						continue
					}
					from, to := rolledBytes, cut
					rolledBytes = cut
					idx := windowIdx
					windowIdx++
					go func() {
						defer func() { <-rollingBusy }()
						s.rollingTranscribe(meeting.ID, pcmPath, from, to, language, idx)
					}()
				default:
				}
			}
		case websocket.TextMessage:
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "stop" {
				goto done
			}
		}
	}
done:
	pcmFile.Close()
	tailIdx := windowIdx
	handedOff = true
	go func() {
		// Wait for any in-flight rolling pass so the tail lands in order.
		rollingBusy <- struct{}{}
		defer func() { <-rollingBusy }()
		defer s.markLive(meeting.ID, false)
		s.finalizeLive(meeting.ID, dir, pcmPath, rolledBytes, pcmBytes, language, tailIdx)
	}()
	conn.WriteJSON(map[string]string{"type": "stopped", "meetingId": meeting.ID})
}

// liveCutPoint picks where the current rolling window should end: the
// quietest >=300ms pause in the last lookback span if it is an actual
// pause, the current end once the window exceeds maxBytes, or 0 meaning
// "no good cut yet, wait for more audio".
func liveCutPoint(f *os.File, rolled, total, chunkBytes, maxBytes, lookbackBytes int64) int64 {
	window := total - rolled
	if window >= maxBytes {
		return total
	}
	from := rolled + chunkBytes - lookbackBytes
	if from < rolled {
		from = rolled
	}
	region := make([]byte, total-from)
	if _, err := f.ReadAt(region, from); err != nil {
		return total // scan failed; behave like the old hard cut
	}
	cut, level := transcribe.QuietestCut(region, 0, len(region))
	if level <= transcribe.SilenceLevel {
		return (from + int64(cut)) &^ 1
	}
	return 0
}

// rollingTranscribe runs one pass over a byte range of the growing PCM
// file. After retries, a failed window becomes a gap segment so the
// transcript shows the hole instead of silently missing minutes.
func (s *Server) rollingTranscribe(meetingID, pcmPath string, from, to int64, language string, windowIdx int) {
	ctx := context.Background()
	bytesPerSec := float64(transcribe.SampleRate * transcribe.BytesPerSample)
	offsetS := float64(from) / bytesPerSec
	durS := float64(to-from) / bytesPerSec

	storeSegments := func(timed []transcribe.TimedSegment) {
		stored := make([]store.TranscriptSegment, 0, len(timed))
		for _, t := range timed {
			speaker := t.Speaker
			stored = append(stored, store.TranscriptSegment{
				MeetingID: meetingID, StartMs: t.StartMs, EndMs: t.EndMs,
				Speaker: &speaker, Text: t.Text, WindowIdx: windowIdx,
			})
		}
		if err := s.st.AppendSegments(ctx, meetingID, stored); err != nil {
			log.Printf("live: rolling store: %v", err)
		}
		payload := make([]map[string]any, 0, len(stored))
		for _, seg := range stored {
			payload = append(payload, map[string]any{
				"id": seg.ID, "startMs": seg.StartMs, "endMs": seg.EndMs,
				"speaker": *seg.Speaker, "text": seg.Text, "windowIdx": seg.WindowIdx,
			})
		}
		s.hub.Broadcast(map[string]any{
			"type": "live-transcript", "meetingId": meetingID, "segments": payload,
		})
	}

	fail := func(err error) {
		log.Printf("live: rolling transcribe: %v", err)
		s.hub.Broadcast(map[string]any{
			"type": "live-error", "meetingId": meetingID, "error": err.Error(),
		})
		storeSegments([]transcribe.TimedSegment{{
			Text:    transcribe.GapText,
			StartMs: int64(offsetS * 1000),
			EndMs:   int64((offsetS + durS) * 1000),
		}})
	}

	var participants []string
	if m, err := s.st.GetMeeting(ctx, meetingID); err == nil {
		participants = m.Participants
	}
	rt, err := jobs.LoadRuntime(ctx, s.cfg, s.st, participants)
	if err != nil {
		fail(err)
		return
	}
	f, err := os.Open(pcmPath)
	if err != nil {
		log.Printf("live: rolling open: %v", err)
		return
	}
	defer f.Close()
	buf := make([]byte, to-from)
	if _, err := f.ReadAt(buf, from); err != nil {
		log.Printf("live: rolling read: %v", err)
		return
	}

	rt.LiveClient.Debug = func(info map[string]any) {
		info["kind"] = "live"
		info["windowIdx"] = windowIdx
		if b, jerr := json.Marshal(info); jerr == nil {
			if derr := s.st.AddTranscribeDebug(ctx, meetingID, string(b)); derr != nil {
				log.Printf("live: add transcribe debug: %v", derr)
			}
		}
	}
	log.Printf("live: rolling pass meeting %s, %.0fs-%.0fs (%d speaker refs)",
		meetingID, offsetS, offsetS+durS, len(rt.LiveClient.Speakers))
	segs, err := transcribe.TranscribeWithRetry(ctx, rt.LiveClient,
		transcribe.EncodeWAV(buf, transcribe.SampleRate), durS, language, 2)
	if err != nil {
		fail(err)
		return
	}
	storeSegments(transcribe.AbsoluteSegments(offsetS, durS, segs))
}

// finalizeLive transcribes whatever tail the rolling passes have not
// covered yet, persists the recording as a WAV (for playback and manual
// retranscription), and marks the meeting completed.
func (s *Server) finalizeLive(meetingID, dir, pcmPath string, rolledBytes, pcmBytes int64, language string, tailWindowIdx int) {
	ctx := context.Background()
	if pcmBytes == 0 {
		log.Printf("live: meeting %s received no audio", meetingID)
		s.st.SetMeetingStatus(ctx, meetingID, "failed")
		return
	}
	if pcmBytes > rolledBytes {
		s.rollingTranscribe(meetingID, pcmPath, rolledBytes, pcmBytes, language, tailWindowIdx)
	}

	durationS := float64(pcmBytes) / float64(transcribe.SampleRate*transcribe.BytesPerSample)
	// Store compressed (~0.35MB/min vs 1.8MB/min); the transcription and
	// clip-extraction paths decode via ffmpeg anyway. WAV is the fallback
	// so a broken encoder never loses a recording.
	audioPath := filepath.Join(dir, "original.m4a")
	if err := audio.RawPCMToM4A(ctx, pcmPath, audioPath); err != nil {
		log.Printf("live: m4a encode failed (%v), keeping WAV", err)
		pcm, rerr := os.ReadFile(pcmPath)
		if rerr != nil {
			log.Printf("live: finalize read pcm: %v", rerr)
			s.st.SetMeetingStatus(ctx, meetingID, "failed")
			return
		}
		audioPath = filepath.Join(dir, "original.wav")
		if werr := os.WriteFile(audioPath, transcribe.EncodeWAV(pcm, transcribe.SampleRate), 0o644); werr != nil {
			log.Printf("live: finalize write wav: %v", werr)
			s.st.SetMeetingStatus(ctx, meetingID, "failed")
			return
		}
	}
	os.Remove(pcmPath)

	if err := s.st.SetMeetingAudioPath(ctx, meetingID, audioPath); err != nil {
		log.Printf("live: finalize: %v", err)
	}
	if err := s.st.SetMeetingDuration(ctx, meetingID, durationS); err != nil {
		log.Printf("live: finalize: %v", err)
	}
	if err := s.st.SetMeetingStatus(ctx, meetingID, "completed"); err != nil {
		log.Printf("live: finalize: %v", err)
	}
	log.Printf("live: meeting %s completed (%.0fs audio)", meetingID, durationS)
	jobs.MaybeAutoSummarize(ctx, s.st, s.hub, meetingID)
	s.hub.Broadcast(map[string]any{
		"type": "job-progress", "meetingId": meetingID,
		"jobType": "live", "status": "completed", "progress": 100,
		"message": "录音转录完成",
	})
}

func (s *Server) markLive(meetingID string, active bool) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if active {
		s.liveActive[meetingID] = true
	} else {
		delete(s.liveActive, meetingID)
	}
}

// claimLive atomically reserves a recording through setup and finalization.
func (s *Server) claimLive(meetingID string) bool {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.liveActive[meetingID] {
		return false
	}
	if s.liveActive == nil {
		s.liveActive = make(map[string]bool)
	}
	s.liveActive[meetingID] = true
	return true
}

func (s *Server) isLiveActive(meetingID string) bool {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	return s.liveActive[meetingID]
}

// stopStuckRecording is the manual way out of a meeting left in
// "recording" — the state a live session exits only over its WebSocket, so
// a crash, a Ctrl-C or a restart strands it there forever. Stranded is not
// harmless: summary.go and truncate both refuse to touch a meeting in this
// status, and resuming the recording is refused too (live.go rejects
// resume while status is recording), so the meeting is effectively locked.
//
// A human asserting "that recording is dead" is the only trustworthy
// signal available. The server cannot decide it alone: `recording` doubles
// as the lock that blocks concurrent resume, and whether its holder is
// still alive is unknowable from another process — nothing stops a second
// instance from opening the same data dir. What the server CAN rule out is
// the case that actually matters, a session live in THIS process, which is
// what liveActive answers.
func (s *Server) stopStuckRecording(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if !s.claimLive(id) {
		writeError(w, http.StatusConflict, "这场录音正在本机录制或收尾中,请等待完成")
		return
	}
	handedOff := false
	defer func() {
		if !handedOff {
			s.markLive(id, false)
		}
	}()
	meeting, err := s.st.GetMeeting(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if meeting.Status != "recording" {
		writeError(w, http.StatusConflict, "该会议不在录制状态,无需停止")
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "meetings", id)
	pcmPath := filepath.Join(dir, "live.pcm")
	fi, statErr := os.Stat(pcmPath)
	if statErr != nil && !os.IsNotExist(statErr) {
		writeError(w, http.StatusInternalServerError, "无法读取录制音频,请检查文件权限后重试")
		return
	}
	if statErr == nil && !fi.Mode().IsRegular() {
		writeError(w, http.StatusInternalServerError, "录制音频不是普通文件")
		return
	}
	hasPCM := statErr == nil && fi.Size() > 0

	// Resumes retain the PREVIOUS AudioPath. A surviving PCM buffer takes
	// precedence: it includes the newly recorded tail that still needs saving.
	if !hasPCM && meeting.AudioPath != nil {
		if audioInfo, err := os.Stat(*meeting.AudioPath); err != nil || !audioInfo.Mode().IsRegular() || audioInfo.Size() == 0 {
			writeError(w, http.StatusInternalServerError, "已保存的录音不可用,无法标记完成")
			return
		}
		if err := s.st.SetMeetingStatus(r.Context(), id, "completed"); err != nil {
			writeStoreError(w, err)
			return
		}
		jobs.MaybeAutoSummarize(r.Context(), s.st, s.hub, id)
		s.hub.Broadcast(map[string]any{
			"type": "job-progress", "meetingId": id,
			"jobType": "live", "status": "completed", "progress": 100,
			"message": "已标记录制结束",
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})
		return
	}

	if !hasPCM {
		// No audio ever landed — same verdict finalizeLive reaches.
		if err := s.st.SetMeetingStatus(r.Context(), id, "failed"); err != nil {
			writeStoreError(w, err)
			return
		}
		s.hub.Broadcast(map[string]any{
			"type": "job-progress", "meetingId": id,
			"jobType": "live", "status": "failed", "progress": 100,
			"message": "录制中断且没有录到音频",
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "failed"})
		return
	}

	// rolledBytes lived in the dead session's memory. The stored segments
	// are the surviving copy of that progress: rolling passes cut on a
	// >=300ms pause and append as they finish, so resuming from the last
	// segment end re-covers only the pause itself, not speech.
	pcmBytes := fi.Size()
	endMs, err := s.st.MaxSegmentEndMs(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	rolledBytes := min(pcmBytes, max(int64(0), endMs)*int64(transcribe.SampleRate*transcribe.BytesPerSample)/1000) &^ 1
	maxWin, err := s.st.MaxSegmentWindow(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	tailIdx := maxWin + 1
	language := "zh"
	if meeting.Language != nil && *meeting.Language != "" {
		language = *meeting.Language
	}

	log.Printf("live: manual stop for stranded meeting %s (%.0fs audio, %.0fs untranscribed)",
		id, float64(pcmBytes)/32000, float64(pcmBytes-rolledBytes)/32000)
	// Encoding 90 minutes and transcribing the tail takes far longer than a
	// request: report acceptance, let the hub announce completion.
	handedOff = true
	go func() {
		defer s.markLive(id, false)
		s.finalizeLive(id, dir, pcmPath, rolledBytes, pcmBytes, language, tailIdx)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":              "finalizing",
		"untranscribedTailMs": (pcmBytes - rolledBytes) * 1000 / 32000,
	})
}
