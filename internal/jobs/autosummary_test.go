package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/ws"
)

func TestAutoSummaryTemplateSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		templates []string
		want      string
	}{
		{"default", []string{"alternative", "standard"}, "standard"},
		{"deleted default", []string{"alternative"}, "alternative"},
		{"no templates", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Connect(ctx, filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			m, err := st.CreateMeeting(ctx, "Synthetic summary test", "en", "", "live")
			if err != nil {
				t.Fatal(err)
			}
			if err := st.AppendSegments(ctx, m.ID, []store.TranscriptSegment{{Text: "Synthetic transcript", EndMs: 1000}}); err != nil {
				t.Fatal(err)
			}
			for _, id := range tc.templates {
				if err := st.UpsertTemplate(ctx, store.SummaryTemplate{ID: id, Name: id, Prompt: "Synthetic prompt"}); err != nil {
					t.Fatal(err)
				}
			}
			MaybeAutoSummarize(ctx, st, ws.NewHub(), m.ID)
			sum, err := st.GetSummary(ctx, m.ID)
			if tc.want == "" {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("no templates: summary = %+v, err = %v", sum, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if sum.Template != tc.want {
				t.Errorf("template = %q, want %q", sum.Template, tc.want)
			}
		})
	}
}
