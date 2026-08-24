package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Comment is a revision item on transcript or summary text. All kinds are
// injected as context when (re)generating the meeting summary, so users
// steer the output by annotating instead of editing prompts.
type Comment struct {
	ID        string    `db:"id" json:"id"`
	MeetingID string    `db:"meeting_id" json:"meetingId"`
	Target    string    `db:"target" json:"target"` // transcript | summary | general
	// Kind: "rewrite" (correction, displayed in place, glossary-exportable),
	// "comment" (opinion, original stays), "delete" (strike the quoted text
	// out of the record), "note" (free-standing remark).
	Kind      string    `db:"kind" json:"kind"`
	Quote     string    `db:"quote" json:"quote"`
	Body      string    `db:"body" json:"body"`
	Author    string    `db:"author" json:"author"`
	// Anchor pins the quote to a position: "seg:<id>" (transcript) or
	// "occ:<n>" (nth occurrence in the summary). Empty = match-first.
	Anchor    string    `db:"anchor" json:"anchor"`
	RevisionGroupID string `db:"revision_group_id" json:"revisionGroupId"`
	CreatedAt time.Time `db:"created_at" json:"createdAt"`
}

func (s *Store) ListComments(ctx context.Context, meetingID string) ([]Comment, error) {
	cs := []Comment{}
	err := s.db.SelectContext(ctx, &cs, `
		SELECT * FROM meeting_comments WHERE meeting_id = $1 ORDER BY created_at`, meetingID)
	return cs, err
}

func (s *Store) AddComment(ctx context.Context, meetingID, target, kind, quote, body, author, anchor, revisionGroupID string) (*Comment, error) {
	var c Comment
	err := s.db.GetContext(ctx, &c, `
		INSERT INTO meeting_comments (id, meeting_id, target, kind, quote, body, author, anchor, revision_group_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *`,
		uuid.NewString(), meetingID, target, kind, quote, body, author, anchor, revisionGroupID)
	return &c, err
}

func (s *Store) GetComment(ctx context.Context, meetingID, id string) (*Comment, error) {
	var c Comment
	err := s.db.GetContext(ctx, &c, `
		SELECT * FROM meeting_comments WHERE meeting_id = $1 AND id = $2`, meetingID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (s *Store) UpdateComment(ctx context.Context, meetingID, id, body string) (*Comment, error) {
	var c Comment
	err := s.db.GetContext(ctx, &c, `
		UPDATE meeting_comments SET body = $3
		WHERE meeting_id = $1 AND id = $2 RETURNING *`, meetingID, id, body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (s *Store) DeleteComment(ctx context.Context, meetingID, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM meeting_comments WHERE meeting_id = $1 AND id = $2`, meetingID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
