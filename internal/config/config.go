// Package config loads server configuration from environment variables.
// Everything has a working default so the binary runs with no setup at all;
// only infrastructure concerns live here, the rest is in the settings UI.
package config

import (
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	// Addr is the HTTP listen address, e.g. ":8090".
	Addr string
	// DatabasePath is the SQLite database file. Defaults to a file inside
	// DataDir so a deployment is one directory: database plus audio.
	DatabasePath string
	// DataDir is the root directory for uploaded/normalized audio files.
	DataDir string
	// APIToken, when non-empty, is required as `Authorization: Bearer <token>`
	// on every API request (and `?token=` for WebSocket). Empty disables auth
	// (local development only).
	APIToken string
	// WebDir is the built frontend (Vite dist). When it exists, the server
	// serves it as an SPA; otherwise it falls back to the embedded dev page.
	WebDir string

	// TranscribeBaseURL is the OpenAI-compatible transcription service,
	// e.g. http://127.0.0.1:7860/v1 (MOSS) or a CUDA+SGLang deployment.
	TranscribeBaseURL string
	TranscribeModel   string
	TranscribeAPIKey  string
	// ChunkSeconds is the audio chunk size submitted per request. Large
	// serial chunks (5-10 min) are required for MOSS-class services: small
	// chunks degrade RTF badly and speaker IDs are only consistent within
	// a single request.
	ChunkSeconds int
	// LiveChunkSeconds is the rolling-transcription window for live
	// recording: once this much untranscribed audio accumulates, one
	// provisional pass runs. Kept large for the same RTF reasons; the
	// definitive speaker-consistent transcript is produced by a full-file
	// pass after recording stops.
	LiveChunkSeconds int
}

func FromEnv() Config {
	dataDir := getenv("MEETING_ROOM_DATA_DIR", "./data")
	return Config{
		Addr:              getenv("MEETING_ROOM_ADDR", ":8090"),
		DatabasePath:      getenv("MEETING_ROOM_DB", filepath.Join(dataDir, "meetingroom.db")),
		DataDir:           dataDir,
		APIToken:          os.Getenv("MEETING_ROOM_API_TOKEN"),
		WebDir:            getenv("MEETING_ROOM_WEB_DIR", "./frontend/dist"),
		TranscribeBaseURL: os.Getenv("TRANSCRIBE_BASE_URL"),
		TranscribeModel:   getenv("TRANSCRIBE_MODEL", "gpt-4o-transcribe-diarize"),
		TranscribeAPIKey:  os.Getenv("TRANSCRIBE_API_KEY"),
		ChunkSeconds:      getenvInt("TRANSCRIBE_CHUNK_SECONDS", 600),
		LiveChunkSeconds:  getenvInt("LIVE_CHUNK_SECONDS", 300),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
