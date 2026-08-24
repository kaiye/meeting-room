# Revision Stack Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make a collapsed multi-revision group read visually as a stack of overlapping cards.

**Architecture:** Reuse `revisionGroups` and `expandedRevisionGroups` in `MeetingDetails.tsx`. Change only the collapsed group markup and classes; expanded items remain the existing individual cards.

**Tech Stack:** React, TypeScript, Tailwind CSS.

---

### Task 1: Render a stacked collapsed group

**Files:**
- Modify: `frontend/src/pages/MeetingDetails.tsx`

**Step 1: Write the failing visual assertion**

Add stack-specific class names and a `×N` group count to the collapsed group markup; before implementing the backing layers, run the TypeScript check.

**Step 2: Verify the initial check fails**

Run: `node ../frontend/node_modules/typescript/bin/tsc --noEmit -p frontend/tsconfig.json`

**Step 3: Implement the minimal visual stack**

Render up to two non-interactive, offset background cards behind the leading card. Keep the leading card clickable, add `×N`, and preserve the existing expanded individual cards.

**Step 4: Verify**

Run: `go test ./...` and the frontend production build.

**Step 5: Commit**

    git add frontend/src/pages/MeetingDetails.tsx
    git commit -m "feat: stack grouped revisions"
