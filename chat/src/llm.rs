//! LLM abstraction: a trait plus an OpenAI-compatible client that supports
//! function/tool calling. The harness runs the loop here: model call → tool
//! calls → results → repeat.

use std::env;

use anyhow::Result;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

// ---- messages ----

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct ToolCall {
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub function: ToolCallFn,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct ToolCallFn {
    pub name: String,
    pub arguments: String, // JSON-encoded arguments string
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct Message {
    pub role: String,
    pub content: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_calls: Option<Vec<ToolCall>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<String>,
}

impl Message {
    pub fn system(content: impl Into<String>) -> Self {
        Self {
            role: "system".into(),
            content: Some(content.into()),
            tool_calls: None,
            tool_call_id: None,
        }
    }

    pub fn user(content: impl Into<String>) -> Self {
        Self {
            role: "user".into(),
            content: Some(content.into()),
            tool_calls: None,
            tool_call_id: None,
        }
    }

    pub fn assistant_tool_calls(calls: Vec<ToolCall>) -> Self {
        Self {
            role: "assistant".into(),
            content: None,
            tool_calls: Some(calls),
            tool_call_id: None,
        }
    }

    pub fn tool(tool_call_id: String, content: String) -> Self {
        Self {
            role: "tool".into(),
            content: Some(content),
            tool_calls: None,
            tool_call_id: Some(tool_call_id),
        }
    }
}

// ---- tools ----

/// A function/tool definition handed to the model.
#[derive(Serialize, Clone, Debug)]
pub struct Tool {
    pub name: String,
    pub description: String,
    pub parameters: Value, // JSON schema object
}

// ---- model response ----

#[derive(Debug)]
pub enum LlmResponse {
    Text(String),
    ToolCalls(Vec<ToolCall>),
}

// ---- trait ----

/// LLM is the interface the harness talks to. Swap implementations (OpenAI,
/// Anthropic, ...) behind this without touching the loop.
#[async_trait]
pub trait LLM: Send + Sync {
    async fn complete(&self, messages: &[Message], tools: &[Tool]) -> Result<LlmResponse>;
}

// ---- OpenAI-compatible client ----

/// OpenAiClient is an OpenAI-compatible chat client (works with Ollama /v1).
pub struct OpenAiClient {
    client: reqwest::Client,
    base_url: String,
    model: String,
    api_key: Option<String>,
    reasoning_effort: Option<String>,
    anthropic_workspace_id: Option<String>,
}

impl OpenAiClient {
    pub fn from_env() -> Self {
        let base_url =
            env::var("LLM_BASE_URL").unwrap_or_else(|_| "http://localhost:11434/v1".to_string());
        let model = env::var("LLM_MODEL").unwrap_or_else(|_| "glm-5.3-flash:cloud".to_string());
        let api_key = env::var("LLM_API_KEY").ok();
        let reasoning_effort = env::var("LLM_REASONING_EFFORT").ok();
        let anthropic_workspace_id = env::var("ANTHROPIC_WORKSPACE_ID").ok();
        Self {
            client: reqwest::Client::new(),
            base_url,
            model,
            api_key,
            reasoning_effort,
            anthropic_workspace_id,
        }
    }
}

#[async_trait]
impl LLM for OpenAiClient {
    async fn complete(&self, messages: &[Message], tools: &[Tool]) -> Result<LlmResponse> {
        let base = self.base_url.trim_end_matches('/');
        let url = format!("{base}/chat/completions");

        let mut body = json!({
            "model": self.model,
            "messages": messages,
            "stream": false,
        });
        if let Some(effort) = &self.reasoning_effort {
            body["reasoning_effort"] = json!(effort);
        }
        if !tools.is_empty() {
            body["tools"] = json!(
                tools
                    .iter()
                    .map(|t| json!({
                        "type": "function",
                        "function": {
                            "name": t.name,
                            "description": t.description,
                            "parameters": t.parameters,
                        }
                    }))
                    .collect::<Vec<_>>()
            );
        }

        let mut req = self.client.post(&url);
        if let Some(key) = &self.api_key {
            req = req.bearer_auth(key);
        }
        if let Some(workspace_id) = &self.anthropic_workspace_id {
            req = req.header("anthropic-workspace-id", workspace_id);
        }

        let resp: Value = req
            .json(&body)
            .send()
            .await?
            .error_for_status()?
            .json()
            .await?;

        let msg = &resp["choices"][0]["message"];

        if let Some(calls) = msg["tool_calls"].as_array() {
            let mut out = Vec::new();
            for c in calls {
                out.push(ToolCall {
                    id: c["id"].as_str().unwrap_or_default().to_string(),
                    kind: c["type"].as_str().unwrap_or("function").to_string(),
                    function: ToolCallFn {
                        name: c["function"]["name"]
                            .as_str()
                            .unwrap_or_default()
                            .to_string(),
                        arguments: c["function"]["arguments"]
                            .as_str()
                            .unwrap_or("{}")
                            .to_string(),
                    },
                });
            }
            return Ok(LlmResponse::ToolCalls(out));
        }

        let text = msg["content"].as_str().unwrap_or_default().to_string();
        Ok(LlmResponse::Text(text))
    }
}
