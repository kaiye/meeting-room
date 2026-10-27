package jobs

import (
	"context"
	"errors"
	"log"

	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/ws"
)

// DefaultTemplate is the builtin used for the automatic first summary.
const DefaultTemplate = "standard"

// MaybeAutoSummarize starts a meeting's first summary once its transcript
// is complete, and does nothing if it already has one.
//
// This used to live in the browser: the server broadcast "transcript done"
// and whatever pages happened to be open reacted. Fire-and-forget in both
// directions — with no page open nothing was generated at all, and with
// four pages open four identical runs were. Deciding here binds the
// summary to the event that actually warrants it.
//
// Callers invoke this before broadcasting the completion, so an open page
// sees the summary already marked "generating" rather than "none yet".
func MaybeAutoSummarize(ctx context.Context, st *store.Store, hub *ws.Hub, meetingID string) {
	switch _, err := st.GetSummary(ctx, meetingID); {
	case err == nil:
		return // already has one (any status) — never clobber
	case !errors.Is(err, store.ErrNotFound):
		log.Printf("auto summary: read summary for %s: %v", meetingID, err)
		return
	}
	// An empty transcript would only produce a failed summary row, and that
	// row would then look like "already has one" forever.
	switch n, err := st.CountSegments(ctx, meetingID); {
	case err != nil:
		log.Printf("auto summary: count segments for %s: %v", meetingID, err)
		return
	case n == 0:
		return
	}
	templates, err := st.ListTemplates(ctx)
	if err != nil {
		log.Printf("auto summary: list templates: %v", err)
		return
	}
	// A deleted default must not leave a permanently failed first summary.
	// With no templates, leave the meeting eligible for a later attempt.
	if len(templates) == 0 {
		return
	}
	template := templates[0].ID
	for _, candidate := range templates {
		if candidate.ID == DefaultTemplate {
			template = DefaultTemplate
			break
		}
	}
	job, err := st.StartSummarizeJob(ctx, meetingID, template, "")
	if err != nil {
		// ErrSummaryBusy means something else got there first: nothing to do.
		if !errors.Is(err, store.ErrSummaryBusy) {
			log.Printf("auto summary: queue job for %s: %v", meetingID, err)
		}
		return
	}
	log.Printf("auto summary: queued job %s for meeting %s", job.ID, meetingID)
	hub.Broadcast(map[string]any{
		"type": "summary-update", "meetingId": meetingID, "status": "generating",
	})
}
