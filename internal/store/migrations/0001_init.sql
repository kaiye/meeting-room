-- Meeting Room schema (SQLite).
--
-- Consolidated from the project's original Postgres migrations. Timestamps are
-- TEXT in SQLite's default 'YYYY-MM-DD HH:MM:SS.SSS' shape: the 'subsec'
-- modifier keeps milliseconds, without which several rows written in the same
-- second would tie and ORDER BY created_at would be unstable. The Go layer only
-- ever reads these columns -- every write goes through a SQL default or an
-- explicit datetime('now','subsec') -- so one format holds for every row.
--
-- ON DELETE CASCADE needs `PRAGMA foreign_keys=ON` per connection (set in the
-- DSN); without it deletes silently orphan their children.

CREATE TABLE IF NOT EXISTS meetings (
    id               TEXT PRIMARY KEY,
    title            TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'created',
    language         TEXT,
    audio_path       TEXT,
    duration_seconds REAL,
    created_by       TEXT,
    source           TEXT NOT NULL DEFAULT 'import',   -- live | import
    -- Guards a manual rename from being overwritten by LLM auto-titling.
    title_customized INTEGER NOT NULL DEFAULT 0,
    -- JSON array: which enrolled voiceprints ride along on this meeting's
    -- transcription requests.
    participants     TEXT NOT NULL DEFAULT '[]',
    -- Background info (calendar invite, agenda) fed to summaries and titling.
    context          TEXT NOT NULL DEFAULT '',
    -- Links a recording to the calendar occurrence it was started from.
    calendar_uid     TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);

CREATE TABLE IF NOT EXISTS transcript_segments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    meeting_id TEXT NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    start_ms   INTEGER NOT NULL,
    end_ms     INTEGER NOT NULL,
    speaker    TEXT,
    text       TEXT NOT NULL,
    -- Which live-recording window produced this segment; raw speaker labels
    -- (A/B/...) are only comparable within one window. -1 = batch import.
    window_idx INTEGER NOT NULL DEFAULT -1
);
CREATE INDEX IF NOT EXISTS idx_segments_meeting ON transcript_segments(meeting_id, start_ms);

CREATE TABLE IF NOT EXISTS summaries (
    meeting_id    TEXT PRIMARY KEY REFERENCES meetings(id) ON DELETE CASCADE,
    template      TEXT NOT NULL,
    custom_prompt TEXT NOT NULL DEFAULT '',
    content       TEXT NOT NULL DEFAULT '',
    model         TEXT,
    status        TEXT NOT NULL DEFAULT 'generating',
    error         TEXT,
    -- What was actually sent to the LLM, for the debug panel.
    prompt        TEXT NOT NULL DEFAULT '',
    input         TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);

CREATE TABLE IF NOT EXISTS summary_templates (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    prompt      TEXT NOT NULL,
    builtin     INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);

-- Application configuration (transcription service, summary LLM, glossary,
-- calendar subscription), one JSON document per key.
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);

-- Team roster. A speaker becomes identifiable once a transcript sentence is
-- labelled with their name, which enrolls the clip as their voiceprint.
CREATE TABLE IF NOT EXISTS speakers (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    aliases           TEXT NOT NULL DEFAULT '[]',   -- JSON array of nicknames
    name_en           TEXT NOT NULL DEFAULT '',
    email             TEXT NOT NULL DEFAULT '',
    source_meeting_id TEXT,
    created_at        DATETIME NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at        DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);

CREATE TABLE IF NOT EXISTS speaker_clips (
    id                TEXT PRIMARY KEY,
    speaker_id        TEXT NOT NULL REFERENCES speakers(id) ON DELETE CASCADE,
    audio_path        TEXT NOT NULL,
    source_meeting_id TEXT,
    seconds           REAL NOT NULL DEFAULT 0,
    created_at        DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE INDEX IF NOT EXISTS speaker_clips_speaker_idx ON speaker_clips(speaker_id, created_at DESC);

-- Revisions on transcript or summary text: rewrite / comment / delete / note.
-- All kinds are injected as context when regenerating the summary.
CREATE TABLE IF NOT EXISTS meeting_comments (
    id                TEXT PRIMARY KEY,
    meeting_id        TEXT NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    target            TEXT NOT NULL DEFAULT 'transcript',  -- transcript | summary | general
    kind              TEXT NOT NULL DEFAULT 'rewrite',
    quote             TEXT NOT NULL DEFAULT '',
    body              TEXT NOT NULL,
    author            TEXT NOT NULL DEFAULT '',
    -- Pins the quote to a position: "seg:<id>" or "occ:<n>".
    anchor            TEXT NOT NULL DEFAULT '',
    -- Ties together the revisions produced by one batch rewrite.
    revision_group_id TEXT NOT NULL DEFAULT '',
    created_at        DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE INDEX IF NOT EXISTS meeting_comments_meeting_idx ON meeting_comments(meeting_id, created_at);

CREATE TABLE IF NOT EXISTS jobs (
    id               TEXT PRIMARY KEY,
    meeting_id       TEXT NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    type             TEXT NOT NULL,                    -- transcribe | summarize
    status           TEXT NOT NULL DEFAULT 'queued',
    progress         INTEGER NOT NULL DEFAULT 0,
    error            TEXT,
    -- Cooperative cancellation: the worker aborts between chunks.
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL DEFAULT (datetime('now','subsec')),
    started_at       DATETIME,
    finished_at      DATETIME
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status, created_at);

-- Debug trails behind the bug icons: what each transcription request and each
-- summary/auto-title generation actually sent, and how long it took.
CREATE TABLE IF NOT EXISTS transcribe_debug (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    meeting_id TEXT NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    info       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE INDEX IF NOT EXISTS transcribe_debug_meeting_idx ON transcribe_debug(meeting_id, id DESC);

CREATE TABLE IF NOT EXISTS summary_debug (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    meeting_id TEXT NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    info       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE INDEX IF NOT EXISTS summary_debug_meeting_idx ON summary_debug(meeting_id, id DESC);
