# Revision Groups Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Group revisions made by one batch rewrite, show them as collapsible sidebar cards, and backfill only the meeting in the supplied screenshot.

**Architecture:** Persist a nullable `revision_group_id` in `meeting_comments`. The browser generates one ID when a rewrite includes selected duplicate matches, and the API passes it through to every inserted record. The sidebar derives groups from that value; ungrouped comments use the existing card UI. Migration 0018 adds the column and assigns deterministic IDs only to eligible existing rewrites in meeting `4e78af4f-29d4-4860-8d5f-976f1c36efd8`.

**Tech Stack:** Go, PostgreSQL migrations, React 18, TypeScript, Tailwind CSS, Vite.

---

### Task 1: Persist the revision group identifier

**Files:**
- Create: `internal/store/migrations/0018_revision_groups.sql`
- Modify: `internal/store/comments.go:15-46`
- Modify: `internal/api/comments.go:29-59`
- Test: `internal/store/comments_test.go`

**Step 1: Write the failing test**

Add a store integration test that creates a rewrite with `revisionGroupID = "batch-a"`, reloads the meeting comments, and expects the same value in the returned `Comment`. Add a companion assertion that an empty value stays empty.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store -run TestCommentRevisionGroup -count=1`

Expected: FAIL because `Comment` and `AddComment` have no group field.

**Step 3: Write minimal implementation**

Add nullable `revision_group_id TEXT` in migration 0018. Add `RevisionGroupID string` to `store.Comment`; extend `AddComment` with a final `revisionGroupID` parameter and insert it as `NULLIF($9, '')`. Extend the POST request body with `revisionGroupId`, trim it, cap it at 64 characters, and pass it to the store. The migration's backfill must update only `meeting_id = '4e78af4f-29d4-4860-8d5f-976f1c36efd8'`, only `kind = 'rewrite'` rows with an empty group ID, and use content plus same-second creation time as the batch key.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store -run TestCommentRevisionGroup -count=1`

Expected: PASS.

**Step 5: Commit**

    git add internal/store/comments.go internal/store/comments_test.go internal/api/comments.go internal/store/migrations/0018_revision_groups.sql
    git commit -m "feat: persist revision groups"

### Task 2: Send a shared identifier for a batch rewrite

**Files:**
- Modify: `frontend/src/api.ts:20-30`
- Modify: `frontend/src/pages/MeetingDetails.tsx:546-571`

**Step 1: Establish the type-check failure**

Extend the frontend `Comment` type with optional `revisionGroupId` and alter the batch-save call site to include it. Run the frontend type check before adding the required request-side implementation.

Run: `npm run build`

Expected: FAIL until the save flow consistently includes the optional field.

**Step 2: Write minimal implementation**

When saving a rewrite with one or more selected batch matches, create a UUID with `crypto.randomUUID()` once. Include it in the original POST and each batch POST. Do not create a group ID for ordinary comments or a rewrite affecting one occurrence only.

**Step 3: Run verification**

Run: `npm run build`

Expected: PASS.

**Step 4: Commit**

    git add frontend/src/api.ts frontend/src/pages/MeetingDetails.tsx
    git commit -m "feat: link batch rewrite records"

### Task 3: Render revision groups as collapsible sidebar cards

**Files:**
- Modify: `frontend/src/pages/MeetingDetails.tsx` (revision sidebar render)

**Step 1: Establish the type-check failure**

Introduce a local `RevisionGroup` view model with group members and collapsed state, then run `npm run build` before its renderer is added.

Expected: FAIL due to the incomplete sidebar renderer.

**Step 2: Write minimal implementation**

Partition rewrite records by non-empty `revisionGroupId` and render a group header with the original style, occurrence count, timestamp, and a Chevron button. Start every group collapsed. Clicking its header toggles only that group; individual cards inside retain their existing locate/edit/delete handlers. Render ungrouped records through the unchanged individual-card path.

**Step 3: Run verification**

Run: `npm run build && go test ./...`

Expected: PASS.

**Step 4: Manual acceptance check**

Open the supplied meeting and confirm that the prior `Monee → Money` and `Voucher Master` batch cards are collapsed, each count is correct, expanding reveals all member rows, and unrelated comments remain individual cards.

**Step 5: Commit**

    git add frontend/src/pages/MeetingDetails.tsx
    git commit -m "feat: collapse grouped revisions"
