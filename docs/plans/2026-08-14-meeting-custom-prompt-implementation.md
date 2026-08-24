# Meeting Custom Prompt Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Let a user save a meeting-specific prompt that is appended to the selected summary template.

**Architecture:** Reuse `Summary.customPrompt` and the existing summarize request. Add a local editor state in `MeetingDetails.tsx`, initialize it from the loaded summary, and send it from `generate`.

**Tech Stack:** React, TypeScript, Go API, existing summary storage.

---

### Task 1: Wire meeting custom prompt through the page

**Files:**
- Modify: `frontend/src/pages/MeetingDetails.tsx`

**Step 1:** Add the prompt-editor state and initialize it from `summary.customPrompt`.

**Step 2:** Add an accessible pencil button and compact editor with save, cancel, and clear actions.

**Step 3:** Send the saved prompt as `customPrompt` in the summary-generation request.

**Step 4:** Run `go test ./...`, TypeScript checking, and production build.

**Step 5:** Commit the feature.
