//! LLM abstraction: a trait plus an OpenAI-compatible client that supports
//! function/tool calling. The harness runs the loop here: model call → tool
//! calls → results → repeat.

use std::env;

use anyhow::{Context, Result, ensure};
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use tokio::sync::mpsc;

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

    async fn complete_stream(
        &self,
        messages: &[Message],
        tools: &[Tool],
        deltas: mpsc::UnboundedSender<String>,
    ) -> Result<LlmResponse> {
        let response = self.complete(messages, tools).await?;
        if let LlmResponse::Text(text) = &response {
            let _ = deltas.send(text.clone());
        }
        Ok(response)
    }
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

    fn request(
        &self,
        messages: &[Message],
        tools: &[Tool],
        stream: bool,
    ) -> reqwest::RequestBuilder {
        let base = self.base_url.trim_end_matches('/');
        let url = format!("{base}/chat/completions");
        let mut body = json!({
            "model": self.model,
            "messages": messages,
            "stream": stream,
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
        let mut request = self.client.post(&url).json(&body);
        if let Some(key) = &self.api_key {
            request = request.bearer_auth(key);
        }
        if let Some(workspace_id) = &self.anthropic_workspace_id {
            request = request.header("anthropic-workspace-id", workspace_id);
        }
        request
    }
}

#[async_trait]
impl LLM for OpenAiClient {
    async fn complete(&self, messages: &[Message], tools: &[Tool]) -> Result<LlmResponse> {
        let resp: Value = self
            .request(messages, tools, false)
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

    async fn complete_stream(
        &self,
        messages: &[Message],
        tools: &[Tool],
        deltas: mpsc::UnboundedSender<String>,
    ) -> Result<LlmResponse> {
        let mut response = self
            .request(messages, tools, true)
            .send()
            .await?
            .error_for_status()?;
        let mut decoder = SseDecoder::default();
        let mut answer = StreamAnswer::default();
        let mut done = false;
        while let Some(chunk) = response.chunk().await? {
            for data in decoder.push(&chunk) {
                if data == "[DONE]" {
                    done = true;
                    break;
                }
                let value: Value = serde_json::from_str(&data).context("parse LLM stream event")?;
                answer.apply(&value, &deltas)?;
            }
            if done {
                break;
            }
        }
        ensure!(done, "LLM stream ended before [DONE]");
        answer.finish()
    }
}

#[derive(Default)]
struct SseDecoder {
    pending: Vec<u8>,
    data: Vec<String>,
}

impl SseDecoder {
    fn push(&mut self, chunk: &[u8]) -> Vec<String> {
        self.pending.extend_from_slice(chunk);
        let mut events = Vec::new();
        while let Some(end) = self.pending.iter().position(|b| *b == b'\n') {
            let mut line: Vec<u8> = self.pending.drain(..=end).collect();
            line.pop();
            if line.last() == Some(&b'\r') {
                line.pop();
            }
            if line.is_empty() {
                if !self.data.is_empty() {
                    events.push(self.data.drain(..).collect::<Vec<_>>().join("\n"));
                }
            } else if let Some(value) = line.strip_prefix(b"data:") {
                let value = value.strip_prefix(b" ").unwrap_or(value);
                self.data.push(String::from_utf8_lossy(value).into_owned());
            }
        }
        events
    }
}

#[derive(Default)]
struct StreamAnswer {
    text: String,
    calls: Vec<ToolCall>,
}

impl StreamAnswer {
    fn apply(&mut self, event: &Value, deltas: &mpsc::UnboundedSender<String>) -> Result<()> {
        let delta = &event["choices"][0]["delta"];
        if let Some(text) = delta["content"].as_str() {
            self.text.push_str(text);
            ensure!(deltas.send(text.to_string()).is_ok(), "chat stream closed");
        }
        if let Some(calls) = delta["tool_calls"].as_array() {
            for part in calls {
                let index = part["index"]
                    .as_u64()
                    .context("tool call delta missing index")? as usize;
                ensure!(
                    index <= self.calls.len(),
                    "tool call delta index out of order"
                );
                if index == self.calls.len() {
                    self.calls.push(ToolCall {
                        id: String::new(),
                        kind: "function".into(),
                        function: ToolCallFn {
                            name: String::new(),
                            arguments: String::new(),
                        },
                    });
                }
                let call = &mut self.calls[index];
                if let Some(id) = part["id"].as_str() {
                    call.id.push_str(id);
                }
                if let Some(kind) = part["type"].as_str() {
                    call.kind = kind.to_string();
                }
                if let Some(name) = part["function"]["name"].as_str() {
                    call.function.name.push_str(name);
                }
                if let Some(args) = part["function"]["arguments"].as_str() {
                    call.function.arguments.push_str(args);
                }
            }
        }
        Ok(())
    }

    fn finish(self) -> Result<LlmResponse> {
        if self.calls.is_empty() {
            return Ok(LlmResponse::Text(self.text));
        }
        for call in &self.calls {
            ensure!(
                !call.id.is_empty() && !call.function.name.is_empty(),
                "incomplete tool call in LLM stream"
            );
        }
        Ok(LlmResponse::ToolCalls(self.calls))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn decodes_split_sse_and_assembles_tool_calls() {
        let mut decoder = SseDecoder::default();
        let mut answer = StreamAnswer::default();
        let (sender, mut receiver) = mpsc::unbounded_channel();
        let input = concat!(
            "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n",
            "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n",
            "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"render\",\"arguments\":\"{\\\"doc\"}}]}}]}\n\n",
            "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"_id\\\":\\\"1\\\"}\"}}]}}]}\n\n",
            "data: [DONE]\n\n"
        );
        for part in input.as_bytes().chunks(7) {
            for data in decoder.push(part) {
                if data != "[DONE]" {
                    answer
                        .apply(&serde_json::from_str::<Value>(&data).unwrap(), &sender)
                        .unwrap();
                }
            }
        }
        assert_eq!(receiver.try_recv().unwrap(), "Hel");
        assert_eq!(receiver.try_recv().unwrap(), "lo");
        let LlmResponse::ToolCalls(calls) = answer.finish().unwrap() else {
            panic!("expected tool call");
        };
        assert_eq!(calls[0].id, "call_1");
        assert_eq!(calls[0].function.name, "render");
        assert_eq!(calls[0].function.arguments, "{\"doc_id\":\"1\"}");
    }
}
