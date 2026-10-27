package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/kaiye/meeting-room/internal/config"
	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/ws"
)

func recoveryServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Connect(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, err := st.CreateMeeting(context.Background(), "Synthetic recovery test", "en", "", "live")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeetingStatus(context.Background(), m.ID, "recording"); err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: config.Config{DataDir: dir}, st: st, hub: ws.NewHub(), liveActive: map[string]bool{}}, m.ID
}

func stopRecording(s *Server, id string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := mux.SetURLVars(httptest.NewRequest(http.MethodPost, "/", nil), map[string]string{"id": id})
	s.stopStuckRecording(w, r)
	return w
}

func TestRecoverRecording(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		name := "fresh"
		if resumed {
			name = "resumed"
		}
		t.Run(name, func(t *testing.T) {
			s, id := recoveryServer(t)
			// Force the deterministic WAV fallback; no external encoder or real audio.
			t.Setenv("PATH", t.TempDir())
			dir := filepath.Join(s.cfg.DataDir, "meetings", id)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			pcmPath := filepath.Join(dir, "live.pcm")
			if err := os.WriteFile(pcmPath, make([]byte, 64000), 0600); err != nil {
				t.Fatal(err)
			}
			if resumed {
				old := filepath.Join(dir, "old.wav")
				if err := os.WriteFile(old, []byte("synthetic old audio"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := s.st.SetMeetingAudioPath(context.Background(), id, old); err != nil {
					t.Fatal(err)
				}
				if err := s.st.AppendSegments(context.Background(), id, []store.TranscriptSegment{{StartMs: 0, EndMs: 1000, Text: "old synthetic segment"}}); err != nil {
					t.Fatal(err)
				}
			}
			entered := make(chan struct{}, 8)
			release := make(chan struct{})
			asr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				<-release
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"text": "synthetic recovered tail"})
			}))
			defer asr.Close()
			s.cfg.TranscribeBaseURL = asr.URL
			s.cfg.TranscribeModel = "test"
			defer func() {
				close(release)
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					m, err := s.st.GetMeeting(context.Background(), id)
					if err == nil && m.Status != "recording" && !s.isLiveActive(id) {
						if m.Status != "completed" || m.DurationSeconds == nil || *m.DurationSeconds != 2 {
							t.Errorf("recovered meeting = %+v, want completed with 2 seconds of audio", m)
						}
						if m.AudioPath == nil {
							t.Error("recovered audio path missing")
						} else if saved, err := os.ReadFile(*m.AudioPath); err != nil || len(saved) != 64044 {
							t.Errorf("saved WAV size = %d, err = %v, want 64044", len(saved), err)
						}
						segments, err := s.st.GetSegments(context.Background(), id)
						want := 1
						if resumed {
							want = 2
						}
						if err != nil || len(segments) != want {
							t.Errorf("segments = %d, err = %v, want %d", len(segments), err, want)
						} else if segments[want-1].EndMs != 2000 {
							t.Errorf("recovered tail ends at %d, want 2000", segments[want-1].EndMs)
						}
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Error("recovery did not finish")
			}()
			first := stopRecording(s, id)
			if first.Code != http.StatusAccepted {
				t.Fatalf("recovery status = %d, want 202", first.Code)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("new PCM tail was not transcribed")
			}
			if second := stopRecording(s, id); second.Code != http.StatusConflict {
				t.Errorf("duplicate recovery status = %d, want 409", second.Code)
			}
		})
	}
}

func TestRecoveryWithoutPCM(t *testing.T) {
	for _, tc := range []struct {
		name  string
		saved bool
		want  string
	}{
		{"no audio", false, "failed"}, {"already encoded", true, "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id := recoveryServer(t)
			if tc.saved {
				path := filepath.Join(s.cfg.DataDir, "saved.wav")
				if err := os.WriteFile(path, []byte("synthetic saved audio"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := s.st.SetMeetingAudioPath(context.Background(), id, path); err != nil {
					t.Fatal(err)
				}
			}
			if w := stopRecording(s, id); w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			m, err := s.st.GetMeeting(context.Background(), id)
			if err != nil || m.Status != tc.want {
				t.Fatalf("meeting = %+v, err = %v, want %s", m, err, tc.want)
			}
			if s.isLiveActive(id) {
				t.Fatal("synchronous recovery leaked session claim")
			}
		})
	}
}

func TestRecoveryFailureReleasesClaim(t *testing.T) {
	s, id := recoveryServer(t)
	if w := stopRecording(s, "missing-meeting"); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if s.isLiveActive("missing-meeting") {
		t.Fatal("missing meeting leaked claim")
	}
	pcmPath := filepath.Join(s.cfg.DataDir, "meetings", id, "live.pcm")
	if err := os.MkdirAll(pcmPath, 0700); err != nil {
		t.Fatal(err)
	}
	if w := stopRecording(s, id); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if s.isLiveActive(id) {
		t.Fatal("invalid PCM leaked claim")
	}
}

func TestStopActiveRecordingIsRejected(t *testing.T) {
	s, id := recoveryServer(t)
	s.markLive(id, true)
	if w := stopRecording(s, id); w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !s.isLiveActive(id) {
		t.Fatal("rejected request released live session")
	}
}
