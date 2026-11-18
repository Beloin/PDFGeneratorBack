-- Legacy Go document tables and current Rust chat history table (Postgres).
-- The active Go document store uses DriverFS; Rust creates chat_messages on startup.

-- Documents: the main server's store (script drafts + versions).
CREATE TABLE IF NOT EXISTS documents (
    id         TEXT PRIMARY KEY,
    type       TEXT NOT NULL DEFAULT 'pdf',
    draft      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS script_versions (
    doc_id     TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    version    INT  NOT NULL,
    script     TEXT NOT NULL,
    pdf_path   TEXT NOT NULL DEFAULT '',
    cover_path TEXT NOT NULL DEFAULT '',
    pptx_path  TEXT NOT NULL DEFAULT '',
    slides_path TEXT NOT NULL DEFAULT '',
    manifest   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (doc_id, version)
);
ALTER TABLE script_versions ADD COLUMN IF NOT EXISTS cover_path TEXT NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT 'pdf';
ALTER TABLE script_versions ADD COLUMN IF NOT EXISTS pptx_path TEXT NOT NULL DEFAULT '';
ALTER TABLE script_versions ADD COLUMN IF NOT EXISTS slides_path TEXT NOT NULL DEFAULT '';

-- Chat messages: the full conversation, shared by the chat harness.
CREATE TABLE IF NOT EXISTS chat_messages (
    id           BIGSERIAL PRIMARY KEY,
    doc_id       TEXT NOT NULL,
    role         TEXT NOT NULL,
    content      TEXT,
    tool_calls   TEXT,
    tool_call_id TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_chat_messages_doc ON chat_messages (doc_id, id);
