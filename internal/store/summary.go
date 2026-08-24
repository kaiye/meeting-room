package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Summary struct {
	MeetingID    string    `db:"meeting_id" json:"meetingId"`
	Template     string    `db:"template" json:"template"`
	CustomPrompt string    `db:"custom_prompt" json:"customPrompt"`
	Content      string    `db:"content" json:"content"`
	Model     *string   `db:"model" json:"model,omitempty"`
	Status    string    `db:"status" json:"status"`
	Error     *string   `db:"error" json:"error,omitempty"`
	// Prompt/Input are the exact system prompt and user message of the
	// latest generation, kept for the debug view.
	Prompt    string    `db:"prompt" json:"prompt"`
	Input     string    `db:"input" json:"input"`
	CreatedAt time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt time.Time `db:"updated_at" json:"updatedAt"`
}

// SetSummaryDebug records what is about to be sent to the LLM.
func (s *Store) SetSummaryDebug(ctx context.Context, meetingID, prompt, input string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE summaries SET prompt = $2, input = $3, updated_at = datetime('now','subsec')
		WHERE meeting_id = $1`, meetingID, prompt, input)
	return err
}

// StartSummary records that a generation with the given template is under
// way; the summarize job reads the template back from this row. The
// previous content is kept so users can keep reading it while the new
// version generates.
func (s *Store) StartSummary(ctx context.Context, meetingID, template, customPrompt string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO summaries (meeting_id, template, custom_prompt, content, status, error)
		VALUES ($1, $2, $3, '', 'generating', NULL)
		ON CONFLICT (meeting_id) DO UPDATE SET
			template = EXCLUDED.template, custom_prompt = EXCLUDED.custom_prompt,
			status = 'generating', error = NULL, updated_at = datetime('now','subsec')`,
		meetingID, template, customPrompt)
	return err
}

func (s *Store) UpdateSummaryPrompt(ctx context.Context, meetingID, template, customPrompt string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE summaries SET template = $2, custom_prompt = $3, updated_at = datetime('now','subsec')
		WHERE meeting_id = $1`, meetingID, template, customPrompt)
	return err
}

// FinishSummary stores the result; on failure/cancel the previous content
// survives so nothing readable is lost.
func (s *Store) FinishSummary(ctx context.Context, meetingID, content, model string, genErr error) error {
	status := "completed"
	var errMsg *string
	if genErr != nil {
		status = "failed"
		m := genErr.Error()
		errMsg = &m
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE summaries SET
			content = CASE WHEN $4 = 'completed' THEN $2 ELSE content END,
			model = $3, status = $4, error = $5, updated_at = datetime('now','subsec')
		WHERE meeting_id = $1`, meetingID, content, model, status, errMsg)
	return err
}

func (s *Store) GetSummary(ctx context.Context, meetingID string) (*Summary, error) {
	var sm Summary
	err := s.db.GetContext(ctx, &sm, `SELECT * FROM summaries WHERE meeting_id = $1`, meetingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &sm, err
}
