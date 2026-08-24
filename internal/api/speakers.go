package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/kaiye/meeting-room/internal/audio"
	"github.com/kaiye/meeting-room/internal/calendar"
	"github.com/kaiye/meeting-room/internal/transcribe"
)

// Voiceprint enrollment is fully automatic from the user's point of view:
// they click a transcript segment's speaker label and type a name; the
// server cuts that segment's audio (clamped to the official 2-10s window)
// out of the stored recording as the reference clip. Subsequent diarized
// transcription requests — including the next rolling pass of a live
// meeting — attach it as known_speaker_references[], so the service labels
// that voice with the real name across chunks.

const (
	refMinSeconds = 2.0
	refMaxSeconds = 10.0
)

func (s *Server) listSpeakers(w http.ResponseWriter, r *http.Request) {
	speakers, err := s.st.ListSpeakers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, speakers)
}

func (s *Server) enrollSpeaker(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MeetingID string `json:"meetingId"`
		SegmentID int64  `json:"segmentId"`
		Name      string `json:"name"`
		// ApplyAll relabels every segment in this meeting that currently
		// carries the same speaker label as the annotated one.
		ApplyAll bool `json:"applyAll"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || body.MeetingID == "" || body.SegmentID == 0 {
		writeError(w, http.StatusBadRequest, "meetingId、segmentId 和 name 都不能为空")
		return
	}
	seg, err := s.st.GetSegment(r.Context(), body.MeetingID, body.SegmentID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	meeting, err := s.st.GetMeeting(r.Context(), body.MeetingID)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	startS, durS, werr := refWindow(float64(seg.StartMs)/1000, float64(seg.EndMs)/1000)
	if werr != nil {
		writeError(w, http.StatusBadRequest, werr.Error())
		return
	}
	var clip []byte
	if meeting.AudioPath != nil {
		clip, err = audio.ExtractClipWAV(r.Context(), *meeting.AudioPath, startS, durS)
	} else {
		// Live recording in progress: cut straight from the raw PCM buffer.
		clip, err = extractLivePCMClip(filepath.Join(s.cfg.DataDir, "meetings", meeting.ID, "live.pcm"), startS, durS)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "截取声纹参考音频失败: "+err.Error())
		return
	}

	clipDir := filepath.Join(s.cfg.DataDir, "speakers")
	if err := os.MkdirAll(clipDir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	clipPath := filepath.Join(clipDir, uuid.NewString()+".wav")
	if err := os.WriteFile(clipPath, clip, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Typed name resolves against canonical names AND aliases (中文名/英文
	// 名/昵称 are the same person). One sample per person: the new
	// annotation replaces any previous ones.
	speaker, err := s.st.EnrollSpeakerName(r.Context(), body.Name)
	if err != nil {
		os.Remove(clipPath)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	newClip, err := s.st.AddSpeakerClip(r.Context(), speaker.ID, clipPath, meeting.ID, durS)
	if err != nil {
		os.Remove(clipPath)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if oldPaths, err := s.st.DeleteSpeakerClipsExcept(r.Context(), speaker.ID, newClip.ID); err != nil {
		log.Printf("api: prune old clips for %s: %v", speaker.Name, err)
	} else {
		for _, p := range oldPaths {
			os.Remove(p)
		}
	}
	oldLabel := ""
	if seg.Speaker != nil {
		oldLabel = *seg.Speaker
	}
	label := speaker.DisplayName() // transcripts use nicknames
	var renamed int64
	scope := "one"
	if body.ApplyAll && oldLabel != "" && oldLabel != label {
		// Real names come from voiceprint matching and are globally stable
		// -> whole meeting. Raw cluster labels (A/B/C) only mean "the same
		// person" within one transcription window -> scope to that window.
		labelIsRealName := false
		if all, err := s.st.ListSpeakers(r.Context()); err == nil {
			for i := range all {
				if all[i].Matches(oldLabel) {
					labelIsRealName = true
					break
				}
			}
		}
		if labelIsRealName || seg.WindowIdx < 0 {
			scope = "meeting"
			renamed, err = s.st.RenameMeetingSpeaker(r.Context(), body.MeetingID, oldLabel, label)
		} else {
			scope = "window"
			renamed, err = s.st.RenameMeetingSpeakerInWindow(r.Context(), body.MeetingID, oldLabel, label, seg.WindowIdx)
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
	} else if err := s.st.UpdateSegmentSpeaker(r.Context(), body.MeetingID, body.SegmentID, label); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"speaker": speaker, "clipSeconds": durS, "renamed": renamed, "scope": scope})
}

// serveSpeakerSample streams the speaker's single (newest) voiceprint
// sample — exactly what transcription requests carry.
func (s *Server) serveSpeakerSample(w http.ResponseWriter, r *http.Request) {
	clips, err := s.st.ListSpeakerClips(r.Context(), mux.Vars(r)["id"])
	if err != nil || len(clips) == 0 {
		writeError(w, http.StatusNotFound, "该说话人还没有声纹样本")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	http.ServeFile(w, r, clips[0].AudioPath)
}

func (s *Server) listSpeakerClips(w http.ResponseWriter, r *http.Request) {
	clips, err := s.st.ListSpeakerClips(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, clips)
}

// serveClipAudio streams a voiceprint sample so users can audibly confirm
// who a speaker entry actually is.
func (s *Server) serveClipAudio(w http.ResponseWriter, r *http.Request) {
	clip, err := s.st.GetSpeakerClip(r.Context(), mux.Vars(r)["clipId"])
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	http.ServeFile(w, r, clip.AudioPath)
}

func (s *Server) deleteSpeakerClip(w http.ResponseWriter, r *http.Request) {
	clip, err := s.st.DeleteSpeakerClip(r.Context(), mux.Vars(r)["clipId"])
	if err != nil {
		writeStoreError(w, err)
		return
	}
	os.Remove(clip.AudioPath)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) updateSpeaker(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string   `json:"name"`
		Aliases []string `json:"aliases"`
		NameEn  string   `json:"nameEn"`
		Email   string   `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	aliases := make([]string, 0, len(body.Aliases))
	for _, a := range body.Aliases {
		if a = strings.TrimSpace(a); a != "" && a != strings.TrimSpace(body.Name) {
			aliases = append(aliases, a)
		}
	}
	if err := s.st.UpdateSpeaker(r.Context(), mux.Vars(r)["id"], strings.TrimSpace(body.Name), aliases,
		strings.TrimSpace(body.NameEn), strings.TrimSpace(body.Email)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// createMember adds a roster entry by hand (no voiceprint yet — that comes
// from annotating a transcript segment later).
func (s *Server) createMember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string   `json:"name"`
		Aliases []string `json:"aliases"`
		NameEn  string   `json:"nameEn"`
		Email   string   `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "中文姓名不能为空")
		return
	}
	name := strings.TrimSpace(body.Name)
	email := strings.TrimSpace(body.Email)
	if existing, err := s.st.ListSpeakers(r.Context()); err == nil {
		for i := range existing {
			if existing[i].Matches(name) || (email != "" && existing[i].Matches(email)) {
				writeError(w, http.StatusConflict, "该成员已存在:"+existing[i].Name)
				return
			}
		}
	}
	sp, _, err := s.st.EnsureMember(r.Context(), name, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	aliases := make([]string, 0, len(body.Aliases))
	for _, a := range body.Aliases {
		if a = strings.TrimSpace(a); a != "" && a != name {
			aliases = append(aliases, a)
		}
	}
	if err := s.st.UpdateSpeaker(r.Context(), sp.ID, name, aliases, strings.TrimSpace(body.NameEn), email); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": sp.ID})
}

// ensureMembers resolves calendar attendees to members, creating
// profile-only entries (no voiceprint) for the ones we've never seen.
func (s *Server) ensureMembers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Attendees []struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"attendees"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	names := make([]string, 0, len(body.Attendees))
	created := 0
	for _, a := range body.Attendees {
		if calendar.IsResource(a.Email, "") {
			continue // meeting rooms are not members
		}
		sp, isNew, err := s.st.EnsureMember(r.Context(), a.Name, a.Email)
		if err != nil {
			continue // skip unusable entries, keep the rest
		}
		if isNew {
			created++
		}
		names = append(names, sp.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names, "created": created})
}

// mergeMembers folds duplicates (same person recognized twice) into one.
func (s *Server) mergeMembers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TargetID  string   `json:"targetId"`
		SourceIDs []string `json:"sourceIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TargetID == "" || len(body.SourceIDs) == 0 {
		writeError(w, http.StatusBadRequest, "targetId 和 sourceIds 不能为空")
		return
	}
	stale, err := s.st.MergeMembers(r.Context(), body.TargetID, body.SourceIDs)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, p := range stale {
		os.Remove(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "merged", "removedSamples": len(stale)})
}

func (s *Server) deleteSpeaker(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	paths, err := s.st.DeleteSpeaker(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, p := range paths {
		os.Remove(p)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// refWindow validates a segment's span as a voiceprint sample: under 2s is
// rejected (too short to be reliable), over 10s is truncated to the first
// 10 seconds — experiments showed longer references do NOT improve
// recognition, so exactly one 2-10s sample is kept per person.
func refWindow(segStartS, segEndS float64) (startS, durS float64, err error) {
	durS = segEndS - segStartS
	if durS < refMinSeconds {
		return 0, 0, fmt.Errorf("这句话不足 %.0f 秒,无法作为声纹样本,请挑一句更长的", refMinSeconds)
	}
	if durS > refMaxSeconds {
		durS = refMaxSeconds
	}
	return segStartS, durS, nil
}

// extractLivePCMClip slices [startS, startS+durS) out of the raw s16le
// live buffer and wraps it as WAV, clamped to what has been recorded.
func extractLivePCMClip(pcmPath string, startS, durS float64) ([]byte, error) {
	f, err := os.Open(pcmPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	bytesPerSec := int64(transcribe.SampleRate * transcribe.BytesPerSample)
	from := int64(startS*float64(bytesPerSec)) &^ 1
	to := (from + int64(durS*float64(bytesPerSec))) &^ 1
	if to > fi.Size() {
		to = fi.Size() &^ 1
	}
	if to-from < int64(refMinSeconds*float64(bytesPerSec)) {
		return nil, fmt.Errorf("该句音频不足 %.0f 秒,无法作为声纹参考", refMinSeconds)
	}
	buf := make([]byte, to-from)
	if _, err := f.ReadAt(buf, from); err != nil {
		return nil, err
	}
	return transcribe.EncodeWAV(buf, transcribe.SampleRate), nil
}
