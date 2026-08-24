package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type SummaryTemplate struct {
	ID          string    `db:"id" json:"id"`
	Name        string    `db:"name" json:"name"`
	Description string    `db:"description" json:"description"`
	Prompt      string    `db:"prompt" json:"prompt"`
	Builtin     bool      `db:"builtin" json:"builtin"`
	CreatedAt   time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt   time.Time `db:"updated_at" json:"updatedAt"`
}

func (s *Store) ListTemplates(ctx context.Context) ([]SummaryTemplate, error) {
	ts := []SummaryTemplate{}
	err := s.db.SelectContext(ctx, &ts, `
		SELECT * FROM summary_templates ORDER BY builtin DESC, created_at`)
	return ts, err
}

func (s *Store) GetTemplate(ctx context.Context, id string) (*SummaryTemplate, error) {
	var t SummaryTemplate
	err := s.db.GetContext(ctx, &t, `SELECT * FROM summary_templates WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

func (s *Store) UpsertTemplate(ctx context.Context, t SummaryTemplate) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO summary_templates (id, name, description, prompt, builtin)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name, description = EXCLUDED.description,
			prompt = EXCLUDED.prompt, updated_at = datetime('now','subsec')`,
		t.ID, t.Name, t.Description, t.Prompt, t.Builtin)
	return err
}

func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM summary_templates WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SeedTemplates inserts built-ins only when absent, so user edits (and
// deliberate deletions... which would be re-seeded — acceptable tradeoff)
// survive restarts. Edits always win because of DO NOTHING.
func (s *Store) SeedTemplates(ctx context.Context, seeds []SummaryTemplate) error {
	for _, t := range seeds {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO summary_templates (id, name, description, prompt, builtin)
			VALUES ($1, $2, $3, $4, TRUE)
			ON CONFLICT (id) DO NOTHING`,
			t.ID, t.Name, t.Description, t.Prompt); err != nil {
			return err
		}
	}
	return nil
}
