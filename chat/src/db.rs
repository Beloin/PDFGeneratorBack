//! Postgres chat persistence: save and load the full conversation.

use std::sync::Arc;

use anyhow::{Context, Result};
use tokio_postgres::{Client, NoTls};

use crate::llm::Message;

const DEFAULT_URL: &str = "postgres://pdfeditor:pdfeditor@localhost:5432/pdfeditor";

const SCHEMA: &str = r#"
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
"#;

/// Connects to Postgres and ensures the schema. Returns None when the database
/// is unreachable so the harness still works without persistence.
pub async fn connect() -> Option<Arc<Client>> {
    let url = std::env::var("DATABASE_URL").unwrap_or_else(|_| DEFAULT_URL.to_string());
    match tokio_postgres::connect(&url, NoTls).await {
        Ok((client, connection)) => {
            tokio::spawn(async move {
                if let Err(e) = connection.await {
                    tracing::error!("postgres connection: {e}");
                }
            });
            if let Err(e) = client.batch_execute(SCHEMA).await {
                tracing::warn!("postgres schema: {e}");
            }
            Some(Arc::new(client))
        }
        Err(e) => {
            tracing::warn!("postgres unavailable ({e}); chat not persisted");
            None
        }
    }
}

/// Loads the persisted conversation for a document, oldest first. System
/// prompts are stored separately by the caller and excluded here.
pub async fn load_messages(client: &Client, doc_id: &str) -> Result<Vec<Message>> {
    let rows = client
        .query(
            "SELECT role, content, tool_calls, tool_call_id FROM chat_messages \
             WHERE doc_id = $1 ORDER BY id",
            &[&doc_id],
        )
        .await?;

    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        let role: String = row.get(0);
        let content: Option<String> = row.get(1);
        let tool_calls_raw: Option<String> = row.get(2);
        let tool_call_id: Option<String> = row.get(3);

        let tool_calls = match tool_calls_raw {
            Some(s) if !s.is_empty() => {
                Some(serde_json::from_str(&s).context("parse persisted tool_calls")?)
            }
            _ => None,
        };

        out.push(Message {
            role,
            content,
            tool_calls,
            tool_call_id,
        });
    }
    Ok(out)
}

/// Saves one message to the conversation.
pub async fn save_message(client: &Client, doc_id: &str, m: &Message) -> Result<()> {
    let tool_calls = m
        .tool_calls
        .as_ref()
        .map(|tc| serde_json::to_string(tc))
        .transpose()?;

    client
        .execute(
            "INSERT INTO chat_messages (doc_id, role, content, tool_calls, tool_call_id) \
             VALUES ($1, $2, $3, $4, $5)",
            &[&doc_id, &m.role, &m.content, &tool_calls, &m.tool_call_id],
        )
        .await?;
    Ok(())
}
