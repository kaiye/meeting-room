package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Speaker is an enrolled person. The row ID is their global unique userid;
// Name is the canonical display name and Aliases (中文名/英文名/昵称) all
// resolve to the same person during enrollment. Exactly one voiceprint
// sample (2-10s, the latest annotation) is kept per person.
type Speaker struct {
	ID         string   `db:"id" json:"id"`
	Name       string   `db:"name" json:"name"`
	AliasesRaw string   `db:"aliases" json:"-"`
	Aliases    []string `db:"-" json:"aliases"`
	NameEn     string   `db:"name_en" json:"nameEn"`
	Email      string   `db:"email" json:"email"`
	// Display is the meeting-facing name (nickname first); computed.
	Display string `db:"-" json:"displayName"`
	ClipCount  int      `db:"clip_count" json:"clipCount"`
	// SampleSeconds is the duration of the (single) newest sample.
	SampleSeconds float64   `db:"sample_seconds" json:"sampleSeconds"`
	CreatedAt     time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt     time.Time `db:"updated_at" json:"updatedAt"`
}

func (sp *Speaker) decodeAliases() {
	sp.Aliases = []string{}
	if sp.AliasesRaw != "" {
		_ = json.Unmarshal([]byte(sp.AliasesRaw), &sp.Aliases)
	}
	sp.Display = sp.DisplayName()
}

// Matches reports whether the given name is this person's canonical name,
// english name, email or any alias (case-insensitive).
func (sp *Speaker) Matches(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, k := range sp.AllNames() {
		if strings.ToLower(k) == name {
			return true
		}
	}
	return false
}

func isCJKName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x4E00 || r > 0x9FFF {
			return false
		}
	}
	return true
}

// DisplayName is what people call this person in meetings — meetings use
// nicknames, not full names. An explicit Chinese alias wins; otherwise the
// nickname is derived from the Chinese name (3 chars -> last two, 2 chars
// -> as-is). Transcript labels and voiceprint names use this.
func (sp *Speaker) DisplayName() string {
	// A Chinese nickname wins; otherwise any explicitly set nickname
	// (a short nickname rather than a full legal name) — the roster's "常用昵称" column is authoritative.
	for _, a := range sp.Aliases {
		if a = strings.TrimSpace(a); isCJKName(a) {
			return a
		}
	}
	for _, a := range sp.Aliases {
		if a = strings.TrimSpace(a); a != "" {
			return a
		}
	}
	// No nickname on file: derive one from the Chinese name.
	if isCJKName(sp.Name) {
		r := []rune(sp.Name)
		if len(r) == 3 {
			return string(r[1:])
		}
		if len(r) == 2 {
			return sp.Name
		}
	}
	return sp.Name
}

// AllNames is every identifier this person answers to.
func (sp *Speaker) AllNames() []string {
	out := []string{sp.Name}
	out = append(out, sp.Aliases...)
	if sp.NameEn != "" {
		out = append(out, sp.NameEn)
	}
	if sp.Email != "" {
		out = append(out, sp.Email)
	}
	return out
}

// SpeakerClip is one voiceprint sample, cut from a labeled segment.
type SpeakerClip struct {
	ID              string    `db:"id" json:"id"`
	SpeakerID       string    `db:"speaker_id" json:"speakerId"`
	AudioPath       string    `db:"audio_path" json:"-"`
	SourceMeetingID *string   `db:"source_meeting_id" json:"sourceMeetingId,omitempty"`
	Seconds         float64   `db:"seconds" json:"seconds"`
	CreatedAt       time.Time `db:"created_at" json:"createdAt"`
}

// ListSpeakers returns speakers newest-updated first, which doubles as the
// priority order when only 4 people's references fit on a request.
func (s *Store) ListSpeakers(ctx context.Context) ([]Speaker, error) {
	sp := []Speaker{}
	err := s.db.SelectContext(ctx, &sp, `
		SELECT s.id, s.name, s.aliases, s.name_en, s.email, s.created_at, s.updated_at,
		       (SELECT count(*) FROM speaker_clips c WHERE c.speaker_id = s.id) AS clip_count,
		       COALESCE((SELECT seconds FROM speaker_clips c WHERE c.speaker_id = s.id
		                 ORDER BY c.created_at DESC LIMIT 1), 0) AS sample_seconds
		FROM speakers s ORDER BY s.updated_at DESC`)
	for i := range sp {
		sp[i].decodeAliases()
	}
	return sp, err
}

// EnrollSpeakerName resolves the typed name against canonical names AND
// aliases (中文名/英文名/昵称 all mean the same person); only when nothing
// matches is a new person created.
func (s *Store) EnrollSpeakerName(ctx context.Context, name string) (*Speaker, error) {
	speakers, err := s.ListSpeakers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range speakers {
		if speakers[i].Matches(name) {
			_, err := s.db.ExecContext(ctx,
				`UPDATE speakers SET updated_at = datetime('now','subsec') WHERE id = $1`, speakers[i].ID)
			return &speakers[i], err
		}
	}
	var sp Speaker
	err = s.db.GetContext(ctx, &sp, `
		INSERT INTO speakers (id, name) VALUES ($1, $2)
		RETURNING id, name, aliases, name_en, email, 0 AS clip_count, 0.0 AS sample_seconds, created_at, updated_at`,
		uuid.NewString(), name)
	sp.decodeAliases()
	return &sp, err
}

// EnsureMember resolves a calendar attendee (display name + email) to an
// existing member or creates a profile-only one (no voiceprint yet).
// Returns the member and whether it was created.
func (s *Store) EnsureMember(ctx context.Context, name, email string) (*Speaker, bool, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" && email == "" {
		return nil, false, errors.New("empty attendee")
	}
	speakers, err := s.ListSpeakers(ctx)
	if err != nil {
		return nil, false, err
	}
	for i := range speakers {
		if (email != "" && speakers[i].Matches(email)) || (name != "" && speakers[i].Matches(name)) {
			// Backfill the email we just learned about.
			if email != "" && speakers[i].Email == "" {
				_, _ = s.db.ExecContext(ctx, `UPDATE speakers SET email = $2 WHERE id = $1`, speakers[i].ID, email)
				speakers[i].Email = email
			}
			return &speakers[i], false, nil
		}
	}
	display := name
	if display == "" {
		display = strings.SplitN(email, "@", 2)[0]
	}
	var sp Speaker
	err = s.db.GetContext(ctx, &sp, `
		INSERT INTO speakers (id, name, email) VALUES ($1, $2, $3)
		RETURNING id, name, aliases, name_en, email, 0 AS clip_count, 0.0 AS sample_seconds, created_at, updated_at`,
		uuid.NewString(), display, email)
	sp.decodeAliases()
	return &sp, true, err
}

// MergeMembers folds sources into target: every name they answered to
// becomes an alias, profile gaps are filled, the newest voiceprint sample
// wins, and their transcript labels are relabeled to the target's name.
// Returns audio paths of discarded samples for cleanup.
func (s *Store) MergeMembers(ctx context.Context, targetID string, sourceIDs []string) ([]string, error) {
	speakers, err := s.ListSpeakers(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]*Speaker{}
	for i := range speakers {
		byID[speakers[i].ID] = &speakers[i]
	}
	target := byID[targetID]
	if target == nil {
		return nil, ErrNotFound
	}

	aliases := append([]string{}, target.Aliases...)
	seen := map[string]bool{strings.ToLower(target.Name): true}
	for _, a := range aliases {
		seen[strings.ToLower(a)] = true
	}
	nameEn, email := target.NameEn, target.Email
	var stale []string

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for _, sid := range sourceIDs {
		src := byID[sid]
		if src == nil || sid == targetID {
			continue
		}
		for _, n := range src.AllNames() {
			if n = strings.TrimSpace(n); n != "" && !seen[strings.ToLower(n)] {
				seen[strings.ToLower(n)] = true
				aliases = append(aliases, n)
			}
		}
		if nameEn == "" {
			nameEn = src.NameEn
		}
		if email == "" {
			email = src.Email
		}
		// Transcript labels of the merged-away identity become the target's.
		if _, err := tx.ExecContext(ctx,
			`UPDATE transcript_segments SET speaker = $2 WHERE speaker = $1`, src.Name, target.Name); err != nil {
			return nil, err
		}
	}

	// Keep only the newest sample across target + sources.
	ids := append([]string{targetID}, sourceIDs...)
	type clipRow struct {
		ID        string `db:"id"`
		AudioPath string `db:"audio_path"`
	}
	var clips []clipRow
	q, args, err := sqlx.In(`SELECT id, audio_path FROM speaker_clips WHERE speaker_id IN (?) ORDER BY created_at DESC`, ids)
	if err != nil {
		return nil, err
	}
	if err := tx.SelectContext(ctx, &clips, tx.Rebind(q), args...); err != nil {
		return nil, err
	}
	for i, c := range clips {
		if i == 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE speaker_clips SET speaker_id = $2 WHERE id = $1`, c.ID, targetID); err != nil {
				return nil, err
			}
			continue
		}
		stale = append(stale, c.AudioPath)
		if _, err := tx.ExecContext(ctx, `DELETE FROM speaker_clips WHERE id = $1`, c.ID); err != nil {
			return nil, err
		}
	}

	raw, err := json.Marshal(aliases)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE speakers SET aliases = $2, name_en = $3, email = $4, updated_at = datetime('now','subsec')
		WHERE id = $1`, targetID, string(raw), nameEn, email); err != nil {
		return nil, err
	}
	for _, sid := range sourceIDs {
		if sid == targetID {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM speakers WHERE id = $1`, sid); err != nil {
			return nil, err
		}
	}
	return stale, tx.Commit()
}

// DeleteSpeakerClipsExcept enforces the one-sample-per-person rule: after
// a new annotation lands, all older samples go. Returns their file paths.
func (s *Store) DeleteSpeakerClipsExcept(ctx context.Context, speakerID, keepClipID string) ([]string, error) {
	paths := []string{}
	if err := s.db.SelectContext(ctx, &paths, `
		SELECT audio_path FROM speaker_clips WHERE speaker_id = $1 AND id <> $2`, speakerID, keepClipID); err != nil {
		return nil, err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM speaker_clips WHERE speaker_id = $1 AND id <> $2`, speakerID, keepClipID)
	return paths, err
}

// UpdateSpeaker sets the member's profile: display name, nicknames,
// english name and email.
func (s *Store) UpdateSpeaker(ctx context.Context, id, name string, aliases []string, nameEn, email string) error {
	raw, err := json.Marshal(aliases)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE speakers SET name = $2, aliases = $3, name_en = $4, email = $5, updated_at = datetime('now','subsec') WHERE id = $1`,
		id, name, string(raw), nameEn, email)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSpeaker removes the speaker (clips cascade) and returns the clip
// paths so the caller can clean up files.
func (s *Store) DeleteSpeaker(ctx context.Context, id string) ([]string, error) {
	paths := []string{}
	if err := s.db.SelectContext(ctx, &paths,
		`SELECT audio_path FROM speaker_clips WHERE speaker_id = $1`, id); err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM speakers WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return paths, nil
}

func (s *Store) AddSpeakerClip(ctx context.Context, speakerID, audioPath, sourceMeetingID string, seconds float64) (*SpeakerClip, error) {
	var src *string
	if sourceMeetingID != "" {
		src = &sourceMeetingID
	}
	var c SpeakerClip
	err := s.db.GetContext(ctx, &c, `
		INSERT INTO speaker_clips (id, speaker_id, audio_path, source_meeting_id, seconds)
		VALUES ($1, $2, $3, $4, $5) RETURNING *`,
		uuid.NewString(), speakerID, audioPath, src, seconds)
	return &c, err
}

// ListSpeakerClips returns a speaker's samples newest first (the order in
// which they are concatenated into the request reference).
func (s *Store) ListSpeakerClips(ctx context.Context, speakerID string) ([]SpeakerClip, error) {
	cs := []SpeakerClip{}
	err := s.db.SelectContext(ctx, &cs, `
		SELECT * FROM speaker_clips WHERE speaker_id = $1 ORDER BY created_at DESC`, speakerID)
	return cs, err
}

func (s *Store) GetSpeakerClip(ctx context.Context, clipID string) (*SpeakerClip, error) {
	var c SpeakerClip
	err := s.db.GetContext(ctx, &c, `SELECT * FROM speaker_clips WHERE id = $1`, clipID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (s *Store) DeleteSpeakerClip(ctx context.Context, clipID string) (*SpeakerClip, error) {
	var c SpeakerClip
	err := s.db.GetContext(ctx, &c, `DELETE FROM speaker_clips WHERE id = $1 RETURNING *`, clipID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}
