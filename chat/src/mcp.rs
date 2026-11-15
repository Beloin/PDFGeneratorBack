//! MCP client: connects to the Go main server over streamable HTTP and lets
//! callers list tools and call tools.

use std::env;

use crate::llm::Tool;
use anyhow::{Context, Result};
use rmcp::model::{
    CallToolRequestParams, ClientCapabilities, ClientConfig, ContentBlock, Implementation,
};
use rmcp::service::RunningService;
use rmcp::transport::streamable_http_client::StreamableHttpClientTransport;
use rmcp::{RoleClient, ServiceExt};
use serde::Serialize;
use serde_json::Value;

#[derive(Serialize)]
pub struct ToolInfo {
    pub name: String,
    pub description: Option<String>,
}

pub fn mcp_url() -> String {
    env::var("MCP_SERVER_URL").unwrap_or_else(|_| "http://localhost:8080/mcp".to_string())
}

pub async fn connect() -> Result<RunningService<RoleClient, ClientConfig>> {
    let transport = StreamableHttpClientTransport::from_uri(mcp_url());
    let client_config = ClientConfig::new(
        ClientCapabilities::default(),
        Implementation::new("pdf-editor-chat", "0.1.0"),
    );
    client_config
        .serve(transport)
        .await
        .context("MCP connect failed")
}

pub async fn list_tools_mcp() -> Result<Vec<ToolInfo>> {
    let client = connect().await?;
    let result = client
        .list_tools(None)
        .await
        .context("MCP list_tools failed")?;
    let out = result
        .tools
        .into_iter()
        .map(|t| ToolInfo {
            name: t.name.into_owned(),
            description: t.description.map(|d| d.into_owned()),
        })
        .collect();
    client.cancel().await.ok();
    Ok(out)
}

/// list_tools_schema returns the MCP tools with their JSON schemas, in the
/// shape the LLM expects for function calling.
pub async fn list_tools_schema() -> Result<Vec<Tool>> {
    let client = connect().await?;
    let result = client
        .list_tools(None)
        .await
        .context("MCP list_tools failed")?;
    let out = result
        .tools
        .into_iter()
        .map(|t| Tool {
            name: t.name.to_string(),
            description: t.description.as_deref().unwrap_or_default().to_string(),
            parameters: t.schema_as_json_value(),
        })
        .collect();
    client.cancel().await.ok();
    Ok(out)
}

pub async fn call_tool_mcp(name: &str, arguments: Value) -> Result<String> {
    let client = connect().await?;
    let mut params = CallToolRequestParams::new(name.to_string());
    params.arguments = arguments.as_object().cloned();
    let result = client
        .call_tool(params)
        .await
        .context("MCP call_tool failed")?;
    let mut texts = Vec::new();
    for item in result.content {
        if let ContentBlock::Text(t) = item {
            texts.push(t.text);
        }
    }
    client.cancel().await.ok();
    Ok(texts.join("\n"))
}
