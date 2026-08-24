package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Connect(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Migrate must be safe to run on every boot; the version table, not
// IF NOT EXISTS, is what makes the second run a no-op.
func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Connect(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for i := 0; i < 3; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
	var n int
	if err := st.db.GetContext(ctx, &n, `SELECT count(*) FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("schema_migrations has %d rows, want 1", n)
	}
}

// SQLite ignores ON DELETE CASCADE unless foreign_keys is on for the
// connection. Without the DSN pragma this passes silently in Postgres and
// leaves orphaned transcripts here, so pin it.
func TestDeleteMeetingCascades(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	m, err := st.CreateMeeting(ctx, "级联测试", "zh", "", "live")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendSegments(ctx, m.ID, []TranscriptSegment{
		{StartMs: 0, EndMs: 1000, Text: "第一句"},
		{StartMs: 1000, EndMs: 2000, Text: "第二句"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddComment(ctx, m.ID, "transcript", "rewrite", "第一句", "改写后", "tester", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateJob(ctx, m.ID, "transcribe"); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteMeeting(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"transcript_segments", "meeting_comments", "jobs"} {
		var n int
		if err := st.db.GetContext(ctx, &n,
			`SELECT count(*) FROM `+table+` WHERE meeting_id = ?`, m.ID); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: %d orphaned rows after deleting the meeting", table, n)
		}
	}
}

// A foreign key pointing at nothing must be refused, not silently stored.
func TestForeignKeyEnforced(t *testing.T) {
	st := testStore(t)
	if _, err := st.CreateJob(context.Background(), "no-such-meeting", "transcribe"); err == nil {
		t.Error("job for a nonexistent meeting was accepted")
	}
}

// Timestamps are DB-generated: they must come back as usable times and order
// consistently with insertion.
func TestTimestampsAndOrdering(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	m, err := st.CreateMeeting(ctx, "时间", "zh", "", "live")
	if err != nil {
		t.Fatal(err)
	}
	if m.CreatedAt.IsZero() {
		t.Error("created_at came back zero")
	}
	if m.CreatedAt.Year() < 2020 {
		t.Errorf("created_at looks unparsed: %v", m.CreatedAt)
	}

	// A batch rewrite writes several revisions in the same instant.
	for _, body := range []string{"一", "二", "三", "四", "五"} {
		if _, err := st.AddComment(ctx, m.ID, "transcript", "rewrite", "q", body, "tester", "", "grp"); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := st.ListComments(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 5 {
		t.Fatalf("got %d comments, want 5", len(cs))
	}
	for i, want := range []string{"一", "二", "三", "四", "五"} {
		if cs[i].Body != want {
			t.Errorf("comment %d = %q, want %q (insertion order not preserved)", i, cs[i].Body, want)
		}
	}
}

// Settings use ON CONFLICT ... EXCLUDED upserts.
func TestSettingsUpsert(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	for _, model := range []string{"first-model", "second-model"} {
		if err := st.SaveTranscribeSettings(ctx, TranscribeSettings{
			BaseURL: "http://127.0.0.1:8100/v1", Model: model,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := st.GetTranscribeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Model != "second-model" {
		t.Errorf("model = %q, want second-model", ts.Model)
	}
	var n int
	st.db.GetContext(ctx, &n, `SELECT count(*) FROM settings WHERE key = 'transcribe'`)
	if n != 1 {
		t.Errorf("upsert produced %d rows, want 1", n)
	}
}

// The background worker writes while HTTP handlers read; without WAL and
// busy_timeout in the DSN most of these writes fail with "database is locked".
func TestConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	m, err := st.CreateMeeting(ctx, "并发", "zh", "", "live")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := st.AppendSegments(ctx, m.ID, []TranscriptSegment{
					{StartMs: int64(w*1000 + i), EndMs: int64(w*1000 + i + 1), Text: "并发写入"},
				}); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
	segs, err := st.GetSegments(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 200 {
		t.Errorf("got %d segments, want 200", len(segs))
	}
}
