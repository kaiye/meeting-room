// Package store is the SQLite persistence layer (sqlx). The whole database is
// a single file next to the audio, so the app runs on a user's own machine
// with nothing to install and nothing leaving it.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	db *sqlx.DB
}

// Connect opens (creating if absent) the SQLite database at path.
//
// The pragmas are not tuning knobs, they are required for correctness:
// WAL lets the background worker write while HTTP requests read (the default
// rollback journal fails ~90% of concurrent writes), busy_timeout waits out
// the moments when writers do collide instead of erroring, and foreign_keys
// turns on the ON DELETE CASCADE the schema relies on -- SQLite ignores
// cascades unless asked, silently orphaning a deleted meeting's transcript.
// synchronous=NORMAL is the standard WAL pairing: durable across app crashes,
// trading only the last commits on sudden power loss.
func Connect(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := "file:" + path + "?" + strings.Join([]string{
		"_pragma=journal_mode(WAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
	}, "&")
	db, err := sqlx.ConnectContext(ctx, "sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	db.SetMaxOpenConns(10)
	return &Store{db: db}, nil
}

// Migrate applies embedded migrations in filename order, once each, recording
// what ran in schema_migrations. SQLite has no "ADD COLUMN IF NOT EXISTS", so
// unlike the Postgres original this cannot re-run every file on each boot.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied := map[string]bool{}
	var names []string
	if err := s.db.SelectContext(ctx, &names, `SELECT name FROM schema_migrations`); err != nil {
		return err
	}
	for _, n := range names {
		applied[n] = true
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}
	sort.Strings(files)

	for _, name := range files {
		if applied[name] {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		// SQLite gives us transactional DDL: a migration that fails halfway
		// leaves no partially-migrated schema behind.
		tx, err := s.db.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

type Meeting struct {
	ID              string   `db:"id" json:"id"`
	Title           string   `db:"title" json:"title"`
	Status          string   `db:"status" json:"status"`
	Language        *string  `db:"language" json:"language,omitempty"`
	AudioPath       *string  `db:"audio_path" json:"-"`
	DurationSeconds *float64 `db:"duration_seconds" json:"durationSeconds,omitempty"`
	CreatedBy       *string  `db:"created_by" json:"createdBy,omitempty"`
	// Source distinguishes live browser recordings from uploaded files.
	Source string `db:"source" json:"source"`
	// TitleCustomized guards manually renamed meetings from LLM auto-titling.
	TitleCustomized bool `db:"title_customized" json:"titleCustomized"`
	// participants limits which enrolled voiceprints ride along on this
	// meeting's transcription requests (empty = all, capped at 4).
	ParticipantsRaw string   `db:"participants" json:"-"`
	Participants    []string `db:"-" json:"participants"`
	// HasAudio tells the frontend whether GET /meetings/{id}/audio will
	// serve (the path itself stays server-side).
	HasAudio bool `db:"-" json:"hasAudio"`
	// Context is per-meeting background info (calendar invite, agenda…),
	// injected into summary generation and AI titling.
	Context string `db:"context" json:"context"`
	// CalendarUID links this recording to the calendar occurrence it was
	// started from (empty for ad-hoc recordings).
	CalendarUID string    `db:"calendar_uid" json:"calendarUid"`
	CreatedAt time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt time.Time `db:"updated_at" json:"updatedAt"`
}

func (m *Meeting) decodeParticipants() {
	m.Participants = []string{}
	if m.ParticipantsRaw != "" {
		_ = json.Unmarshal([]byte(m.ParticipantsRaw), &m.Participants)
	}
	m.HasAudio = m.AudioPath != nil
}

type TranscriptSegment struct {
	ID        int64   `db:"id" json:"id"`
	MeetingID string  `db:"meeting_id" json:"-"`
	StartMs   int64   `db:"start_ms" json:"startMs"`
	EndMs     int64   `db:"end_ms" json:"endMs"`
	Speaker   *string `db:"speaker" json:"speaker,omitempty"`
	Text      string  `db:"text" json:"text"`
	// WindowIdx is the transcription window this segment came from; raw
	// cluster labels (A/B/C) are only comparable within one window.
	WindowIdx int `db:"window_idx" json:"windowIdx"`
}

type Job struct {
	ID              string     `db:"id" json:"id"`
	MeetingID       string     `db:"meeting_id" json:"meetingId"`
	Type            string     `db:"type" json:"type"`
	Status          string     `db:"status" json:"status"`
	Progress        int        `db:"progress" json:"progress"`
	Error           *string    `db:"error" json:"error,omitempty"`
	CancelRequested bool       `db:"cancel_requested" json:"cancelRequested"`
	CreatedAt       time.Time  `db:"created_at" json:"createdAt"`
	StartedAt       *time.Time `db:"started_at" json:"startedAt,omitempty"`
	FinishedAt      *time.Time `db:"finished_at" json:"finishedAt,omitempty"`
}

var ErrNotFound = errors.New("not found")

func (s *Store) CreateMeeting(ctx context.Context, title, language, audioPath, source string) (*Meeting, error) {
	m := &Meeting{ID: uuid.NewString(), Title: title, Status: "created"}
	var lang, path *string
	if language != "" {
		lang = &language
	}
	if audioPath != "" {
		path = &audioPath
	}
	if source == "" {
		source = "import"
	}
	err := s.db.GetContext(ctx, m, `
		INSERT INTO meetings (id, title, language, audio_path, source)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING *`, m.ID, title, lang, path, source)
	m.decodeParticipants()
	return m, err
}

func (s *Store) ListMeetings(ctx context.Context) ([]Meeting, error) {
	ms := []Meeting{}
	err := s.db.SelectContext(ctx, &ms, `SELECT * FROM meetings ORDER BY created_at DESC`)
	for i := range ms {
		ms[i].decodeParticipants()
	}
	return ms, err
}

func (s *Store) GetMeeting(ctx context.Context, id string) (*Meeting, error) {
	var m Meeting
	err := s.db.GetContext(ctx, &m, `SELECT * FROM meetings WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	m.decodeParticipants()
	return &m, err
}

func (s *Store) SetMeetingCalendarUID(ctx context.Context, id, uid string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE meetings SET calendar_uid = $2, updated_at = datetime('now','subsec') WHERE id = $1`, id, uid)
	return err
}

func (s *Store) SetMeetingContext(ctx context.Context, id, context string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE meetings SET context = $2, updated_at = datetime('now','subsec') WHERE id = $1`, id, context)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RequestTranscribeCancel flags this meeting's pending/running
// transcription jobs; the worker notices between chunks and aborts.
func (s *Store) RequestTranscribeCancel(ctx context.Context, meetingID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET cancel_requested = TRUE
		WHERE meeting_id = $1 AND status IN ('queued','running') AND type <> 'summarize'`, meetingID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RequestSummarizeCancel flags the meeting's pending/running summary job.
func (s *Store) RequestSummarizeCancel(ctx context.Context, meetingID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET cancel_requested = TRUE
		WHERE meeting_id = $1 AND status IN ('queued','running') AND type = 'summarize'`, meetingID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) JobCancelRequested(ctx context.Context, jobID string) (bool, error) {
	var c bool
	err := s.db.GetContext(ctx, &c, `SELECT cancel_requested FROM jobs WHERE id = $1`, jobID)
	return c, err
}

func (s *Store) SetMeetingParticipants(ctx context.Context, id string, names []string) error {
	raw, err := json.Marshal(names)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE meetings SET participants = $2, updated_at = datetime('now','subsec') WHERE id = $1`, id, string(raw))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplyAutoTitle stores an LLM-generated title. Without force it never
// overwrites a title the user typed themselves; with force (the explicit
// "AI rename" button) it applies and puts the title back under auto
// management. Returns whether it was applied.
func (s *Store) ApplyAutoTitle(ctx context.Context, id, title string, force bool) (bool, error) {
	q := `UPDATE meetings SET title = $2, title_customized = FALSE, updated_at = datetime('now','subsec') WHERE id = $1`
	if !force {
		q += ` AND NOT title_customized`
	}
	res, err := s.db.ExecContext(ctx, q, id, title)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) DeleteMeeting(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM meetings WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RenameMeeting(ctx context.Context, id, title string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE meetings SET title = $2, title_customized = TRUE, updated_at = datetime('now','subsec') WHERE id = $1`, id, title)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetMeetingStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE meetings SET status = $2, updated_at = datetime('now','subsec') WHERE id = $1`, id, status)
	return err
}

func (s *Store) SetMeetingAudioPath(ctx context.Context, id, path string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE meetings SET audio_path = $2, updated_at = datetime('now','subsec') WHERE id = $1`, id, path)
	return err
}

func (s *Store) SetMeetingDuration(ctx context.Context, id string, seconds float64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE meetings SET duration_seconds = $2, updated_at = datetime('now','subsec') WHERE id = $1`, id, seconds)
	return err
}

func (s *Store) GetSegments(ctx context.Context, meetingID string) ([]TranscriptSegment, error) {
	segs := []TranscriptSegment{}
	err := s.db.SelectContext(ctx, &segs, `
		SELECT * FROM transcript_segments
		WHERE meeting_id = $1 ORDER BY start_ms, id`, meetingID)
	return segs, err
}

// AppendSegments adds provisional segments during live recording and fills
// in the generated IDs (the frontend needs them for speaker labeling); the
// final full-file pass later replaces them via ReplaceSegments.
func (s *Store) AppendSegments(ctx context.Context, meetingID string, segs []TranscriptSegment) error {
	for i := range segs {
		var speaker *string
		if segs[i].Speaker != nil && *segs[i].Speaker != "" {
			speaker = segs[i].Speaker
		}
		if err := s.db.GetContext(ctx, &segs[i].ID, `
			INSERT INTO transcript_segments (meeting_id, start_ms, end_ms, speaker, text, window_idx)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			meetingID, segs[i].StartMs, segs[i].EndMs, speaker, segs[i].Text, segs[i].WindowIdx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetSegment(ctx context.Context, meetingID string, id int64) (*TranscriptSegment, error) {
	var seg TranscriptSegment
	err := s.db.GetContext(ctx, &seg, `
		SELECT * FROM transcript_segments WHERE meeting_id = $1 AND id = $2`, meetingID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &seg, err
}

// MaxSegmentWindow returns the highest transcription window index used in
// a meeting (-1 when empty); resumed recordings continue numbering after it.
func (s *Store) MaxSegmentWindow(ctx context.Context, meetingID string) (int, error) {
	var n int
	err := s.db.GetContext(ctx, &n, `
		SELECT COALESCE(MAX(window_idx), -1) FROM transcript_segments WHERE meeting_id = $1`, meetingID)
	return n, err
}

// DeleteSegmentsFrom drops all transcript segments at or after the given
// timestamp (used when the recording is truncated).
func (s *Store) DeleteSegmentsFrom(ctx context.Context, meetingID string, fromMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM transcript_segments WHERE meeting_id = $1 AND start_ms >= $2`, meetingID, fromMs)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// RenameMeetingSpeaker relabels every segment of one speaker label in a
// meeting (batch "every A in this window is actually Alice"). Safe for voiceprint-derived
// real names, which are globally stable. Returns affected count.
func (s *Store) RenameMeetingSpeaker(ctx context.Context, meetingID, from, to string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE transcript_segments SET speaker = $3
		WHERE meeting_id = $1 AND speaker = $2`, meetingID, from, to)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// RenameMeetingSpeakerInWindow is the raw-cluster-label variant: A/B/C are
// only the same person within one transcription window, so the batch is
// scoped to that window.
func (s *Store) RenameMeetingSpeakerInWindow(ctx context.Context, meetingID, from, to string, windowIdx int) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE transcript_segments SET speaker = $3
		WHERE meeting_id = $1 AND speaker = $2 AND window_idx = $4`, meetingID, from, to, windowIdx)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) UpdateSegmentSpeaker(ctx context.Context, meetingID string, id int64, speaker string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE transcript_segments SET speaker = $3
		WHERE meeting_id = $1 AND id = $2`, meetingID, id, speaker)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceSegments atomically swaps a meeting's transcript, so both import
// and retranscribe share one write path.
func (s *Store) ReplaceSegments(ctx context.Context, meetingID string, segs []TranscriptSegment) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM transcript_segments WHERE meeting_id = $1`, meetingID); err != nil {
		return err
	}
	for _, seg := range segs {
		var speaker *string
		if seg.Speaker != nil && *seg.Speaker != "" {
			speaker = seg.Speaker
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transcript_segments (meeting_id, start_ms, end_ms, speaker, text, window_idx)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			meetingID, seg.StartMs, seg.EndMs, speaker, seg.Text, seg.WindowIdx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CreateJob(ctx context.Context, meetingID, jobType string) (*Job, error) {
	j := &Job{}
	err := s.db.GetContext(ctx, j, `
		INSERT INTO jobs (id, meeting_id, type)
		VALUES ($1, $2, $3)
		RETURNING *`, uuid.NewString(), meetingID, jobType)
	return j, err
}

func (s *Store) GetLatestJob(ctx context.Context, meetingID string) (*Job, error) {
	var j Job
	err := s.db.GetContext(ctx, &j, `
		SELECT * FROM jobs WHERE meeting_id = $1
		ORDER BY created_at DESC LIMIT 1`, meetingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &j, err
}

// RequeueOrphanedJobs returns jobs stuck in 'running' (left behind by a
// crashed/killed server) to the queue. Called once at worker startup;
// assumes a single server instance owns the queue.
func (s *Store) RequeueOrphanedJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'queued', progress = 0, started_at = NULL
		WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ClaimNextJob atomically picks the oldest queued job and marks it running.
// One UPDATE ... RETURNING does the whole thing: SQLite has no FOR UPDATE
// SKIP LOCKED, and a SELECT-then-UPDATE pair inside a deferred transaction
// would have to upgrade a read lock to a write lock, which busy_timeout does
// not retry. rowid breaks ties so the queue stays FIFO when several jobs are
// created within the same millisecond.
func (s *Store) ClaimNextJob(ctx context.Context) (*Job, error) {
	var j Job
	err := s.db.GetContext(ctx, &j, `
		UPDATE jobs SET status = 'running', started_at = datetime('now','subsec')
		WHERE id = (
			SELECT id FROM jobs WHERE status = 'queued'
			ORDER BY created_at, rowid LIMIT 1
		)
		RETURNING *`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) UpdateJobProgress(ctx context.Context, id string, progress int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET progress = $2 WHERE id = $1`, id, progress)
	return err
}

func (s *Store) FinishJob(ctx context.Context, id, status string, jobErr error) error {
	var msg *string
	if jobErr != nil {
		m := jobErr.Error()
		msg = &m
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = $2, error = $3, finished_at = datetime('now','subsec'),
			progress = CASE WHEN $2 = 'completed' THEN 100 ELSE progress END
		WHERE id = $1`, id, status, msg)
	return err
}
