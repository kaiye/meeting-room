# Revision groups design

## Goal

Treat all revisions created by one batch rewrite as one user-visible group. The
revision sidebar presents that group as a collapsible item, while preserving the
existing locate, edit, and delete operations for every individual revision.

## Scope

- New batch rewrites receive a shared, explicit group identifier.
- The selected meeting shown in the supplied screenshot receives a one-time
  backfill for its existing batch records only.
- Existing records in every other meeting retain their current ungrouped state.

## Data model and API

Add a nullable `revision_group_id` to comment storage and expose it through the
comment API. The frontend creates one UUID for a batch rewrite and sends it for
the original selection and every selected matching occurrence. Ordinary
comments and single rewrites do not receive a group ID.

The one-time backfill is constrained to the supplied meeting ID. Within that
meeting, matching rewrite records created in the same batch are assigned a
shared identifier. It must be idempotent and only populate empty group IDs.

## Sidebar behavior

Rows with the same non-empty group ID are rendered as one group card. Its
header shows the revision type, number of occurrences, timestamp, and a
disclosure control. Groups begin collapsed and expand on click. Ungrouped
records continue to render exactly as individual cards.

## Error handling

If a group ID is absent, malformed, or appears only once, the UI displays the
record normally. A backfill failure must not block viewing or creating comments.

## Verification

Tests cover persistence and API round-tripping of a group ID, creation of a
batch that shares one ID, grouping behavior, expand/collapse state, and
compatibility for ungrouped records.
