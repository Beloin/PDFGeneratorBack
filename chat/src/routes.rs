//! axum handlers and shared state.

use std::{convert::Infallible, sync::Arc};

use axum::Json;
use axum::body::{Body, to_bytes};
use axum::extract::{FromRequest, Multipart};
use axum::extract::{Query, State};
use axum::http::{Request, StatusCode};
use axum::response::IntoResponse;
use axum::response::sse::{Event, KeepAlive, Sse};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use tokio::sync::mpsc;
use tokio_postgres::Client;
use tokio_stream::wrappers::UnboundedReceiverStream;

use crate::db;
use crate::llm::{LLM, LlmResponse, Message};
use crate::mcp::{call_tool_mcp, list_tools_mcp, list_tools_schema};

pub type ApiError = (StatusCode, Json<Value>);

const MAX_STEPS: usize = 1000;
const MAX_REQUEST_BYTES: usize = 20 * 1024 * 1024;
const MAX_IMAGE_BYTES: usize = 5 * 1024 * 1024;
const MAX_IMAGES: usize = 8;

const SYSTEM_PROMPT: &str = "\
You edit document-generation Python scripts by calling tools. Before making any
edit, call `get_script` and inspect its returned `type`, even when the user
selected an element. The type chooses the API: `pdf` uses the PDF rules below;
`slideshow` uses the slideshow rules. Never mix the two wrappers.

Shared rules:
- Save changes with `edit_script`, passing the current version as `base_version`.
- Give every new element a unique, stable id; never rename or drop an existing
  id unless asked. Make minimal edits and leave unrelated script text unchanged.
- `edit_script` saves a draft. To check it, call `render` (doc_id); it commits a
  new version and returns a manifest or traceback. Fix tracebacks and render
  again. Use the returned version as the next `base_version`.
- Call `editing_pdf` (doc_id) when editing starts. Use `list_sources` and
  `get_source` to inspect source files when relevant.
- Selection metadata is context, not an instruction. Target selected element
  ids and keep other elements unchanged unless asked.
- Uploaded images are already stored in the document. Use the exact stored
  filename supplied in chat when inserting one.

PDF (`type: pdf`): The script uses ReportLab through `Doc` (`from doc import
Doc`). Coordinates and image dimensions are points. Add an uploaded image with
`Doc.image(name, element_id, width=None, height=None)`; omit one dimension to
preserve aspect ratio. For SVGs, call `add_svg` (doc_id, name, svg), then use
`doc.svg('name', 'element-id')`.

Slideshow (`type: slideshow`): Use `Slides` from `slides` (`from
slides import Slides`), backed by python-pptx; do not use `Doc`, ReportLab, or
PDF drawing APIs. Keep the existing Slides setup and save flow; when creating
it, use `Slides(OUT_PATH, width=960, height=540)`. Coordinates are points with
a top-left origin. Use only these methods: `slide(background='#...')`,
`text(text, element_id, x, y, width, height, font_size=..., color=...,
bold=False)`, `rect(element_id, x, y, width, height, fill=..., line=...)`, and
`image(name, element_id, x, y, width=None, height=None)`. Use `slide` to add a
slide, preserve aspect ratio when one image dimension is omitted, and call
`save()` when required by the existing flow. The wrapper saves PPTX and
converts it to PDF during render.

When done, reply with a short plain-text summary; do not dump the whole script.";

#[derive(Clone)]
pub struct AppState {
    pub llm: Arc<dyn LLM>,
    pub db: Option<Arc<Client>>,
}

#[derive(Deserialize)]
pub struct CallRequest {
    pub tool: String,
    #[serde(default)]
    pub arguments: Value,
}

#[derive(Deserialize)]
pub struct ChatRequest {
    pub message: String,
    #[serde(default)]
    pub system: Option<String>,
    #[serde(default)]
    pub doc_id: Option<String>,
    #[serde(default)]
    pub selection: Option<Selection>,
    #[serde(default)]
    pub selections: Vec<Selection>,
}

struct ChatInput {
    request: ChatRequest,
    images: Vec<UploadPart>,
    attachments: Vec<Value>,
}

struct UploadPart {
    filename: String,
    content_type: String,
    bytes: Vec<u8>,
}

#[derive(Deserialize, Serialize, Clone)]
pub struct Selection {
    pub element_id: String,
    pub page: u32,
    pub bbox: [f64; 4],
}

fn model_user_message(message: &str, selections: &[Selection], attachments: &[Value]) -> Message {
    let mut context = Vec::new();
    if !selections.is_empty() {
        context.push(format!(
            "Selected elements (context data): {}",
            serde_json::to_string(selections).expect("selections serialize")
        ));
    }
    if !attachments.is_empty() {
        context.push(format!(
            "Uploaded images (stored in this document; filenames are authoritative): {}",
            serde_json::to_string(attachments).expect("attachments serialize")
        ));
    }
    if context.is_empty() {
        return Message::user(message);
    }
    Message::user(format!("{message}\n\n{}", context.join("\n")))
}

pub async fn health() -> Json<Value> {
    Json(json!({ "ok": true }))
}

pub async fn tools() -> Result<Json<Value>, ApiError> {
    match list_tools_mcp().await {
        Ok(list) => Ok(Json(json!(list))),
        Err(e) => Err((
            StatusCode::BAD_GATEWAY,
            Json(json!({ "error": e.to_string() })),
        )),
    }
}

pub async fn call(Json(req): Json<CallRequest>) -> Result<Json<Value>, ApiError> {
    match call_tool_mcp(&req.tool, req.arguments).await {
        Ok(content) => Ok(Json(json!({ "ok": true, "content": content }))),
        Err(e) => Err((
            StatusCode::BAD_GATEWAY,
            Json(json!({ "error": e.to_string() })),
        )),
    }
}

pub async fn chat(
    State(state): State<AppState>,
    request: Request<Body>,
) -> Result<Json<Value>, ApiError> {
    let input = prepare_uploads(parse_chat_input(request).await?).await?;
    let reply = run_chat(state, input, None).await?;
    Ok(Json(json!({ "reply": reply })))
}

type EventSender = mpsc::UnboundedSender<Result<Event, Infallible>>;

fn bad_request<T>(message: &str) -> Result<T, ApiError> {
    Err((StatusCode::BAD_REQUEST, Json(json!({"error": message}))))
}

fn combined_selections(single: Option<&Selection>, many: &[Selection]) -> Vec<Selection> {
    let mut selections = many.to_vec();
    if let Some(single) = single {
        if !selections.iter().any(|s| {
            s.element_id == single.element_id && s.page == single.page && s.bbox == single.bbox
        }) {
            selections.insert(0, single.clone());
        }
    }
    selections
}

async fn parse_chat_input(request: Request<Body>) -> Result<ChatInput, ApiError> {
    let content_type = request
        .headers()
        .get(axum::http::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_owned();
    if content_type.starts_with("application/json") {
        let body = to_bytes(request.into_body(), MAX_REQUEST_BYTES)
            .await
            .map_err(|_| {
                (
                    StatusCode::PAYLOAD_TOO_LARGE,
                    Json(json!({"error":"request exceeds 20 MiB"})),
                )
            })?;
        let request: ChatRequest = serde_json::from_slice(&body).map_err(|e| {
            (
                StatusCode::BAD_REQUEST,
                Json(json!({"error":format!("invalid JSON: {e}")})),
            )
        })?;
        return Ok(ChatInput {
            request,
            images: vec![],
            attachments: vec![],
        });
    }
    if !content_type.starts_with("multipart/form-data") {
        return bad_request("expected application/json or multipart/form-data");
    }
    let mut multipart = Multipart::from_request(request, &()).await.map_err(|e| {
        (
            StatusCode::BAD_REQUEST,
            Json(json!({"error":format!("invalid multipart request: {e}")})),
        )
    })?;
    let mut fields = serde_json::Map::new();
    let mut images = Vec::new();
    while let Some(field) = multipart.next_field().await.map_err(|e| {
        (
            e.status(),
            Json(json!({"error":format!("invalid multipart field: {e}")})),
        )
    })? {
        let field_name = field.name().unwrap_or("").to_owned();
        if field_name == "images" {
            let Some(filename) = field.file_name().map(str::to_owned) else {
                return bad_request("images fields must be files");
            };
            let content_type = field.content_type().unwrap_or("").to_owned();
            let bytes = field.bytes().await.map_err(|e| {
                (
                    StatusCode::BAD_REQUEST,
                    Json(json!({"error":format!("invalid image field: {e}")})),
                )
            })?;
            if images.len() >= MAX_IMAGES {
                return Err((
                    StatusCode::PAYLOAD_TOO_LARGE,
                    Json(json!({"error":"at most 8 images are allowed"})),
                ));
            }
            if bytes.len() > MAX_IMAGE_BYTES {
                return Err((
                    StatusCode::PAYLOAD_TOO_LARGE,
                    Json(json!({"error":"each image must be at most 5 MiB"})),
                ));
            }
            let valid = (content_type == "image/png" && bytes.starts_with(b"\x89PNG\r\n\x1a\n"))
                || (content_type == "image/jpeg" && bytes.starts_with(b"\xff\xd8\xff"));
            if !valid {
                return bad_request("images must be valid PNG or JPEG files");
            }
            images.push(UploadPart {
                filename,
                content_type,
                bytes: bytes.to_vec(),
            });
        } else if !field_name.is_empty() {
            let bytes = field.bytes().await.map_err(|e| {
                (
                    StatusCode::BAD_REQUEST,
                    Json(json!({"error":format!("invalid text field: {e}")})),
                )
            })?;
            let value = String::from_utf8(bytes.to_vec()).map_err(|_| {
                (
                    StatusCode::BAD_REQUEST,
                    Json(json!({"error":"multipart text fields must be UTF-8"})),
                )
            })?;
            if ["message", "doc_id", "system", "selection", "selections"]
                .contains(&field_name.as_str())
            {
                fields.insert(field_name.to_string(), Value::String(value));
            }
        }
    }
    let get = |key: &str| fields.get(key).and_then(Value::as_str);
    let message = get("message")
        .ok_or_else(|| {
            (
                StatusCode::BAD_REQUEST,
                Json(json!({"error":"message required"})),
            )
        })?
        .to_string();
    let parse_json_field = |key: &str| -> Result<Option<Value>, ApiError> {
        get(key)
            .map(|s| {
                serde_json::from_str(s).map_err(|e| {
                    (
                        StatusCode::BAD_REQUEST,
                        Json(json!({"error":format!("invalid {key} JSON: {e}")})),
                    )
                })
            })
            .transpose()
    };
    let selection = parse_json_field("selection")?
        .map(serde_json::from_value)
        .transpose()
        .map_err(|e| {
            (
                StatusCode::BAD_REQUEST,
                Json(json!({"error":format!("invalid selection: {e}")})),
            )
        })?;
    let selections = parse_json_field("selections")?
        .map(serde_json::from_value)
        .transpose()
        .map_err(|e| {
            (
                StatusCode::BAD_REQUEST,
                Json(json!({"error":format!("invalid selections: {e}")})),
            )
        })?
        .unwrap_or_default();
    Ok(ChatInput {
        request: ChatRequest {
            message,
            system: get("system").map(str::to_owned),
            doc_id: get("doc_id").map(str::to_owned),
            selection,
            selections,
        },
        images,
        attachments: vec![],
    })
}

async fn upload_image(doc_id: &str, image: UploadPart) -> Result<Value, ApiError> {
    let url = image_upload_url(&crate::mcp::mcp_url(), doc_id)?;
    let part = reqwest::multipart::Part::bytes(image.bytes)
        .file_name(image.filename)
        .mime_str(&image.content_type)
        .map_err(|e| {
            (
                StatusCode::BAD_REQUEST,
                Json(json!({"error":e.to_string()})),
            )
        })?;
    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .build()
        .map_err(|e| {
            (
                StatusCode::BAD_GATEWAY,
                Json(json!({"error":e.to_string()})),
            )
        })?;
    let mut request = client
        .post(url)
        .multipart(reqwest::multipart::Form::new().part("file", part));
    if let Ok(token) = std::env::var("AUTH_TOKEN")
        && !token.is_empty()
    {
        request = request.bearer_auth(token);
    }
    let response = request.send().await.map_err(|e| {
        (
            StatusCode::BAD_GATEWAY,
            Json(json!({"error":format!("image upload failed: {e}")})),
        )
    })?;
    let status = response.status();
    let value: Value = response
        .json()
        .await
        .unwrap_or_else(|_| json!({"error":"invalid image upload response"}));
    if !status.is_success() {
        return Err((
            StatusCode::BAD_GATEWAY,
            Json(json!({"error":"image upload rejected", "details":value})),
        ));
    }
    Ok(value)
}

fn image_upload_url(mcp: &str, doc_id: &str) -> Result<reqwest::Url, ApiError> {
    let origin = mcp
        .strip_suffix("/mcp")
        .unwrap_or(mcp.trim_end_matches('/'));
    let mut url =
        reqwest::Url::parse(&format!("{}/docs", origin.trim_end_matches('/'))).map_err(|e| {
            (
                StatusCode::BAD_GATEWAY,
                Json(json!({"error":e.to_string()})),
            )
        })?;
    url.path_segments_mut()
        .map_err(|_| {
            (
                StatusCode::BAD_GATEWAY,
                Json(json!({"error":"invalid MCP_SERVER_URL"})),
            )
        })?
        .push(doc_id)
        .push("images");
    Ok(url)
}

async fn prepare_uploads(mut input: ChatInput) -> Result<ChatInput, ApiError> {
    if input.images.is_empty() {
        return Ok(input);
    }
    let Some(doc_id) = input.request.doc_id.as_deref() else {
        return bad_request("doc_id required when uploading images");
    };
    for image in std::mem::take(&mut input.images) {
        input.attachments.push(upload_image(doc_id, image).await?);
    }
    Ok(input)
}

fn emit(sender: &EventSender, name: &'static str, data: Value) -> Result<(), ApiError> {
    sender
        .send(Ok(Event::default()
            .event(name)
            .json_data(data)
            .expect("JSON value serializes")))
        .map_err(|_| {
            (
                StatusCode::BAD_GATEWAY,
                Json(json!({ "error": "chat stream closed" })),
            )
        })
}

pub async fn chat_stream(
    State(state): State<AppState>,
    request: Request<Body>,
) -> Result<impl IntoResponse, ApiError> {
    // Do uploads before creating the SSE response so failures are HTTP errors.
    let input = prepare_uploads(parse_chat_input(request).await?).await?;
    let (sender, receiver) = mpsc::unbounded_channel();
    tokio::spawn(async move {
        match run_chat(state, input, Some(sender.clone())).await {
            Ok(reply) => {
                let _ = emit(&sender, "done", json!({ "reply": reply }));
            }
            Err((_, Json(error))) => {
                let _ = emit(&sender, "error", error);
            }
        }
    });
    Ok(Sse::new(UnboundedReceiverStream::new(receiver)).keep_alive(KeepAlive::default()))
}

async fn run_chat(
    state: AppState,
    input: ChatInput,
    sender: Option<EventSender>,
) -> Result<String, ApiError> {
    let ChatInput {
        request: req,
        attachments,
        ..
    } = input;
    let selections = combined_selections(req.selection.as_ref(), &req.selections);
    let mut messages = vec![Message::system(SYSTEM_PROMPT)];
    if let Some(doc_id) = &req.doc_id {
        messages.push(Message::system(format!(
            "The current document id is \"{doc_id}\"."
        )));
    }
    if let Some(sys) = req.system {
        messages.push(Message::system(sys));
    }

    // Rehydrate the persisted conversation (system prompts live above).
    if let (Some(db), Some(doc_id)) = (&state.db, &req.doc_id) {
        match db::load_messages(db, doc_id).await {
            Ok(history) => messages.extend(history),
            Err(e) => tracing::warn!("load chat history: {e}"),
        }
    }

    let user = model_user_message(&req.message, &selections, &attachments);
    persist(&state.db, &req.doc_id, &user).await;
    messages.push(user);

    let tools = list_tools_schema().await.map_err(|e| {
        (
            StatusCode::BAD_GATEWAY,
            Json(json!({ "error": e.to_string() })),
        )
    })?;

    let mut steps = 0;
    loop {
        steps += 1;
        if steps > MAX_STEPS {
            return Err((
                StatusCode::BAD_GATEWAY,
                Json(json!({ "error": format!("stopped after {MAX_STEPS} steps") })),
            ));
        }

        let resp = if let Some(sender) = &sender {
            let (delta_sender, mut delta_receiver) = mpsc::unbounded_channel();
            let event_sender = sender.clone();
            let forward = tokio::spawn(async move {
                while let Some(delta) = delta_receiver.recv().await {
                    if emit(&event_sender, "message", json!({ "delta": delta })).is_err() {
                        break;
                    }
                }
            });
            let result = state
                .llm
                .complete_stream(&messages, &tools, delta_sender)
                .await;
            let _ = forward.await;
            result
        } else {
            state.llm.complete(&messages, &tools).await
        }
        .map_err(|e| {
            (
                StatusCode::BAD_GATEWAY,
                Json(json!({ "error": e.to_string() })),
            )
        })?;

        match resp {
            LlmResponse::Text(text) => {
                let m = Message {
                    role: "assistant".into(),
                    content: Some(text.clone()),
                    tool_calls: None,
                    tool_call_id: None,
                };
                persist(&state.db, &req.doc_id, &m).await;
                return Ok(text);
            }
            LlmResponse::ToolCalls(calls) => {
                let am = Message::assistant_tool_calls(calls.clone());
                persist(&state.db, &req.doc_id, &am).await;
                messages.push(am);
                for c in &calls {
                    let args: Value =
                        serde_json::from_str(&c.function.arguments).unwrap_or(Value::Null);
                    if let Some(sender) = &sender {
                        emit(
                            sender,
                            "tool_call",
                            json!({
                                "id": c.id, "name": c.function.name, "arguments": args
                            }),
                        )?;
                    }
                    let result = call_tool_mcp(&c.function.name, args)
                        .await
                        .unwrap_or_else(|e| format!("error: {e}"));
                    if let Some(sender) = &sender {
                        emit(
                            sender,
                            "tool_result",
                            json!({
                                "id": c.id, "name": c.function.name, "content": result
                            }),
                        )?;
                    }
                    let tm = Message::tool(c.id.clone(), result);
                    persist(&state.db, &req.doc_id, &tm).await;
                    messages.push(tm);
                }
            }
        }
    }
}

async fn persist(db: &Option<Arc<Client>>, doc_id: &Option<String>, m: &Message) {
    if let (Some(db), Some(doc_id)) = (db, doc_id) {
        if let Err(e) = db::save_message(db, doc_id, m).await {
            tracing::warn!("save chat message: {e}");
        }
    }
}

#[derive(Deserialize)]
pub struct HistoryQuery {
    #[serde(default)]
    pub doc_id: Option<String>,
}

/// Returns the persisted conversation for a document.
pub async fn chat_history(
    State(state): State<AppState>,
    Query(q): Query<HistoryQuery>,
) -> Result<Json<Value>, ApiError> {
    let Some(doc_id) = q.doc_id else {
        return Err((
            StatusCode::BAD_REQUEST,
            Json(json!({ "error": "doc_id required" })),
        ));
    };
    if doc_id.trim().is_empty() {
        return Err((
            StatusCode::BAD_REQUEST,
            Json(json!({ "error": "doc_id required" })),
        ));
    }
    match &state.db {
        Some(db) => match db::load_messages(db, &doc_id).await {
            Ok(messages) => Ok(Json(json!({ "doc_id": doc_id, "messages": messages }))),
            Err(e) => Err((
                StatusCode::SERVICE_UNAVAILABLE,
                Json(json!({ "error": e.to_string() })),
            )),
        },
        None => Err((
            StatusCode::SERVICE_UNAVAILABLE,
            Json(json!({ "error": "chat history persistence unavailable" })),
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::llm::{LlmResponse, Tool};
    use async_trait::async_trait;
    use axum::body::Body;
    use axum::http::{Request, header};

    #[tokio::test]
    async fn sse_delivers_each_event_before_the_next_is_sent() {
        use tokio_stream::StreamExt;

        let (sender, receiver) = mpsc::unbounded_channel();
        let response = Sse::new(UnboundedReceiverStream::new(receiver)).into_response();
        assert_eq!(
            response.headers()[header::CONTENT_TYPE],
            "text/event-stream"
        );
        let mut body = response.into_body().into_data_stream();
        let events = [
            (
                "tool_call",
                json!({"id":"call-1", "name":"render", "arguments":{"doc_id":"doc-1"}}),
            ),
            (
                "tool_result",
                json!({"id":"call-1", "name":"render", "content":"rendered"}),
            ),
            ("message", json!({"delta":"Updated\nslide ✓"})),
            ("done", json!({"reply":"Updated\nslide ✓"})),
        ];
        for (name, payload) in events {
            emit(&sender, name, payload.clone()).unwrap();
            // Await the current event before emitting another: no batch buffering.
            let chunk = body.next().await.unwrap().unwrap();
            let encoded = std::str::from_utf8(&chunk).unwrap();
            assert!(encoded.contains(&format!("event: {name}\n")), "{encoded}");
            let data = encoded
                .lines()
                .find_map(|line| line.strip_prefix("data: "))
                .unwrap();
            assert_eq!(serde_json::from_str::<Value>(data).unwrap(), payload);
            assert!(encoded.ends_with("\n\n"));
        }
        drop(sender);
        assert!(body.next().await.is_none());
    }

    #[tokio::test]
    async fn sse_error_is_delivered_and_stream_closes() {
        use tokio_stream::StreamExt;

        let (sender, receiver) = mpsc::unbounded_channel();
        let mut body = Sse::new(UnboundedReceiverStream::new(receiver))
            .into_response()
            .into_body()
            .into_data_stream();
        emit(&sender, "error", json!({"error":"provider failed"})).unwrap();
        drop(sender);
        let chunk = body.next().await.unwrap().unwrap();
        let encoded = std::str::from_utf8(&chunk).unwrap();
        assert!(encoded.contains("event: error\n"));
        assert!(encoded.contains("provider failed"));
        assert!(!encoded.contains("event: done"));
        assert!(body.next().await.is_none());
    }

    struct TestLlm;
    #[async_trait]
    impl LLM for TestLlm {
        async fn complete(&self, _: &[Message], _: &[Tool]) -> anyhow::Result<LlmResponse> {
            Ok(LlmResponse::Text("ok".into()))
        }
    }

    fn state_without_db() -> AppState {
        AppState {
            llm: Arc::new(TestLlm),
            db: None,
        }
    }

    #[test]
    fn selection_reaches_model_in_user_context() {
        let req: ChatRequest = serde_json::from_value(json!({
            "message": "hello",
            "selection": {
                "element_id": "slide1-break",
                "page": 1,
                "bbox": [78, 78, 882, 286]
            }
        }))
        .unwrap();

        let content = model_user_message(
            &req.message,
            &combined_selections(req.selection.as_ref(), &req.selections),
            &[],
        )
        .content
        .unwrap();
        assert_eq!(
            content,
            "hello\n\nSelected elements (context data): [{\"element_id\":\"slide1-break\",\"page\":1,\"bbox\":[78.0,78.0,882.0,286.0]}]"
        );
    }

    #[test]
    fn system_prompt_selects_api_from_script_type_before_edits() {
        assert!(SYSTEM_PROMPT.contains("call `get_script` and inspect its returned `type`"));
        assert!(SYSTEM_PROMPT.contains("`pdf` uses the PDF rules"));
        assert!(SYSTEM_PROMPT.contains("`slideshow` uses the slideshow rules"));
        assert!(SYSTEM_PROMPT.contains("from doc import"));
        assert!(SYSTEM_PROMPT.contains("slides import Slides"));
        assert!(SYSTEM_PROMPT.contains("python-pptx"));
        assert!(SYSTEM_PROMPT.contains("Coordinates are points with"));
        assert!(SYSTEM_PROMPT.contains("Slides(OUT_PATH, width=960, height=540)"));
        assert!(SYSTEM_PROMPT.contains("rect(element_id, x, y, width, height"));
    }

    #[test]
    fn missing_selection_keeps_message_unchanged() {
        let req: ChatRequest = serde_json::from_value(json!({ "message": "hello" })).unwrap();
        assert_eq!(
            model_user_message(&req.message, &[], &[])
                .content
                .as_deref(),
            Some("hello")
        );
    }

    #[test]
    fn multi_selection_is_included_and_legacy_selection_is_merged() {
        let req: ChatRequest = serde_json::from_value(json!({"message":"edit", "selection":{"element_id":"a","page":1,"bbox":[0,0,1,1]}, "selections":[{"element_id":"b","page":2,"bbox":[1,2,3,4]}]})).unwrap();
        let selected = combined_selections(req.selection.as_ref(), &req.selections);
        assert_eq!(selected.len(), 2);
        let content = model_user_message(&req.message, &selected, &[])
            .content
            .unwrap();
        assert!(content.contains("\"element_id\":\"a\""));
        assert!(content.contains("\"element_id\":\"b\""));
    }

    #[test]
    fn uploaded_references_are_in_context_for_model_and_history() {
        let attachment =
            json!({"name":"chart.png","key":"docs/d1/chart.png","url":"/files/chart.png"});
        let message = model_user_message("insert this", &[], std::slice::from_ref(&attachment));
        let stored = serde_json::to_value(message).unwrap();
        assert!(stored["content"].as_str().unwrap().contains("chart.png"));
        assert!(
            stored["content"]
                .as_str()
                .unwrap()
                .contains("docs/d1/chart.png")
        );
    }

    #[test]
    fn image_upload_url_replaces_mcp_suffix_and_encodes_document_id() {
        let url = image_upload_url("http://localhost:8080/mcp", "doc/id").unwrap();
        assert_eq!(url.as_str(), "http://localhost:8080/docs/doc%2Fid/images");
    }

    #[tokio::test]
    async fn multipart_parsing_accepts_selections_and_repeated_png_fields() {
        let boundary = "unit-test-boundary";
        let image = b"\x89PNG\r\n\x1a\nimage";
        let mut body = format!(
            "--{boundary}\r\nContent-Disposition: form-data; name=\"message\"\r\n\r\ninsert it\r\n--{boundary}\r\nContent-Disposition: form-data; name=\"doc_id\"\r\n\r\ndoc-1\r\n--{boundary}\r\nContent-Disposition: form-data; name=\"selections\"\r\n\r\n[{{\"element_id\":\"a\",\"page\":1,\"bbox\":[0,0,1,1]}}]\r\n--{boundary}\r\nContent-Disposition: form-data; name=\"images\"; filename=\"one.png\"\r\nContent-Type: image/png\r\n\r\n"
        ).into_bytes();
        body.extend_from_slice(image);
        body.extend_from_slice(format!("\r\n--{boundary}--\r\n").as_bytes());
        let request = Request::builder()
            .header(
                header::CONTENT_TYPE,
                format!("multipart/form-data; boundary={boundary}"),
            )
            .body(Body::from(body))
            .unwrap();
        let input = parse_chat_input(request).await.unwrap();
        assert_eq!(input.request.message, "insert it");
        assert_eq!(input.request.selections.len(), 1);
        assert_eq!(input.images.len(), 1);
        assert_eq!(input.images[0].filename, "one.png");
    }

    #[tokio::test]
    async fn oversized_json_is_rejected_before_chat_processing() {
        let body = format!("{{\"message\":\"{}\"}}", "x".repeat(MAX_REQUEST_BYTES));
        let request = Request::builder()
            .header(header::CONTENT_TYPE, "application/json")
            .body(Body::from(body))
            .unwrap();
        assert_eq!(
            parse_chat_input(request).await.err().unwrap().0,
            StatusCode::PAYLOAD_TOO_LARGE
        );
    }

    #[tokio::test]
    async fn history_rejects_empty_id_and_reports_unavailable_persistence() {
        let empty = chat_history(
            State(state_without_db()),
            Query(HistoryQuery {
                doc_id: Some("  ".into()),
            }),
        )
        .await
        .unwrap_err();
        assert_eq!(empty.0, StatusCode::BAD_REQUEST);
        let unavailable = chat_history(
            State(state_without_db()),
            Query(HistoryQuery {
                doc_id: Some("doc-1".into()),
            }),
        )
        .await
        .unwrap_err();
        assert_eq!(unavailable.0, StatusCode::SERVICE_UNAVAILABLE);
    }

    #[tokio::test]
    async fn image_upload_requires_document_id() {
        let input = ChatInput {
            request: ChatRequest {
                message: "x".into(),
                system: None,
                doc_id: None,
                selection: None,
                selections: vec![],
            },
            images: vec![UploadPart {
                filename: "x.png".into(),
                content_type: "image/png".into(),
                bytes: b"\x89PNG\r\n\x1a\n".to_vec(),
            }],
            attachments: vec![],
        };
        assert_eq!(
            prepare_uploads(input).await.err().unwrap().0,
            StatusCode::BAD_REQUEST
        );
    }
}
