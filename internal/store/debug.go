package store

import (
	"context"
	"time"
)

// TranscribeDebug is one recorded transcription request (endpoint + fields
// + outcome), kept so users can verify exactly what was sent.
type TranscribeDebug struct {
	ID        int64     `db:"id" json:"id"`
	MeetingID string    `db:"meeting_id" json:"-"`
	Info      string    `db:"info" json:"info"`
	CreatedAt time.Time `db:"created_at" json:"createdAt"`
}

// AddTranscribeDebug appends a record and prunes the meeting's log to the
// most recent 100 entries.
func (s *Store) AddTranscribeDebug(ctx context.Context, meetingID, info string) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO transcribe_debug (meeting_id, info) VALUES ($1, $2)`, meetingID, info); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM transcribe_debug WHERE meeting_id = $1 AND id NOT IN (
			SELECT id FROM transcribe_debug WHERE meeting_id = $1 ORDER BY id DESC LIMIT 100)`, meetingID)
	return err
}

func (s *Store) ListTranscribeDebug(ctx context.Context, meetingID string) ([]TranscribeDebug, error) {
	ds := []TranscribeDebug{}
	err := s.db.SelectContext(ctx, &ds, `
		SELECT * FROM transcribe_debug WHERE meeting_id = $1 ORDER BY id DESC LIMIT 100`, meetingID)
	return ds, err
}

// AddSummaryDebug appends a summary-generation record (prompt, params,
// duration, outcome), pruned to the most recent 50 per meeting.
func (s *Store) AddSummaryDebug(ctx context.Context, meetingID, info string) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO summary_debug (meeting_id, info) VALUES ($1, $2)`, meetingID, info); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM summary_debug WHERE meeting_id = $1 AND id NOT IN (
			SELECT id FROM summary_debug WHERE meeting_id = $1 ORDER BY id DESC LIMIT 50)`, meetingID)
	return err
}

func (s *Store) ListSummaryDebug(ctx context.Context, meetingID string) ([]TranscribeDebug, error) {
	ds := []TranscribeDebug{}
	err := s.db.SelectContext(ctx, &ds, `
		SELECT * FROM summary_debug WHERE meeting_id = $1 ORDER BY id DESC LIMIT 50`, meetingID)
	return ds, err
}
