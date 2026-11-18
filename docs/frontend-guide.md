# Frontend integration guide

Go serves project data and WebSocket events on `http://localhost:8080`. Rust serves chat on `http://localhost:3000`. This repository does not contain the frontend.

## Template gallery and projects

1. Fetch `GET /templates` and display each `{id, name, cover_url}`.
2. On template click, call `POST /docs` with `{"template":"report","title":"My report"}`. The title is optional. The response arrives after version 1 renders. Open `pdf_url`, `cover_url`, and `manifest_url`; slideshow projects also return `pptx_url`. `slides_url` is an additive scene data endpoint.
3. Fetch `GET /docs` to show projects, newest update first. Use `GET /docs/{id}` to reopen one. `PATCH /docs/{id}` with `{"title":"New title"}` renames it; `DELETE /docs/{id}` removes it. Use the returned PDF and manifest URLs; after a failed edit, they point to `rendered_version` while `version` remains the script head.

No demo projects are created at startup. Templates are independent from projects: editing a project never changes its source template. A failed initial render returns `500` and leaves no project to list.

## Viewer and updates

Use the PDF viewer for both PDF and slideshow projects. Fetch `pdf_url`, `manifest_url`, and `cover_url` from project metadata; slideshow metadata also includes `pptx_url` for an editable PowerPoint download. The `slides_url` scene JSON is an additive API for clients that need per-element data; the frontend does not need a separate slideshow renderer. The existing manifest remains the selection source and uses `[x0, y0, x1, y1]` in PDF points from the bottom-left. Convert y with `page_height - y`, then scale by viewer zoom. One element ID can appear on several pages.

The default cover for both types comes from the converted PDF page and is returned as PNG. The optional slideshow scene response uses top-left point coordinates and includes text baselines and version-pinned image URLs. The Vera font endpoints are available to clients that consume scene JSON directly.

```js
const ws = new WebSocket("ws://localhost:8080/ws");
ws.onopen = () => ws.send(JSON.stringify({ v: 1, type: "subscribe", doc_id: project.doc_id }));
```

Show loading on `updating_pdf`. `pdf_updated` remains the event name for both types. On every update, fetch `pdf_url`, `manifest_url`, and `cover_url`; for slideshows also refresh the editable PowerPoint download from `pptx_url`. Slideshow events additionally identify `project_type: "slideshow"` and include `slides_url` for clients that use scene JSON. On `pdf_failed`, show the error and keep the last usable output. Ignore versions already displayed. After reconnect, resubscribe and compare `subscribed.version` with the displayed version.

## Chat selection

```json
{
  "doc_id": "PROJECT_ID",
  "message": "Make the title blue",
  "selections": [
    {"element_id":"title","page":1,"bbox":[78,741.89,517.28,763.89]},
    {"element_id":"subtitle","page":1,"bbox":[78,700,517,730]}
  ]
}
```

Send this to Rust `POST /chat/stream` with `fetch` and read the `text/event-stream` response. `EventSource` cannot send the POST body. `selections` is optional context for the model. The older single `selection` object remains supported; set `doc_id` for editing and chat history.

| SSE event | JSON data | Frontend action |
|---|---|---|
| `message` | `{"delta":"..."}` | Append the text to the assistant message. |
| `tool_call` | `{"id":"...","name":"...","arguments":{...}}` | Show which tool the model called. |
| `tool_result` | `{"id":"...","name":"...","content":"..."}` | Mark the tool complete; show the result if useful. |
| `done` | `{"reply":"..."}` | Finish the assistant message. |
| `error` | `{"error":"..."}` | Show the error and stop loading. |

Tool events can repeat before the final message. Rendering events may arrive over Go's WebSocket before `done`. `POST /chat` still returns one JSON object for older clients. `GET /chat/history?doc_id=...` returns saved messages if Rust can reach Postgres.

## Reference sources

Upload a PDF or UTF-8 Markdown file to Go before asking the model to use it:

```js
const source = new FormData();
source.append("file", sourceFile);
const response = await fetch(`/docs/${docId}/sources`, { method: "POST", body: source });
if (!response.ok) throw new Error(await response.text());
```

Use `GET /docs/{id}/sources` to list sources and `GET /docs/{id}/sources/{sourceID}` to read one. PDF uploads are converted to Markdown text. Scanned PDFs without extractable text are rejected; there is no OCR. Sources belong to the project, and the AI can read them using `list_sources` and `get_source`.

## Images attached to chat

Both Rust chat endpoints also accept multipart form data:

```js
const form = new FormData();
form.append("doc_id", docId);
form.append("message", "Add these images to the selected slide");
form.append("selections", JSON.stringify(selectedElements));
for (const file of imageFiles) form.append("images", file);
const response = await fetch("/chat/stream", { method: "POST", body: form });
if (!response.ok) throw new Error(await response.text());
// Read SSE events from response.body as with JSON chat.
```

Do not set `Content-Type` manually; the browser supplies the multipart boundary. Images require `doc_id`. PNG and JPEG files are stored with generated names and passed to the model as asset references. The model adds them to the generating script; uploading alone does not render a new version. In PDF projects the model uses `doc.image`; in slideshows it uses `Slides.image`. Scene image URLs identify the asset snapshot for a specific version, for example `/docs/PROJECT_ID/images/chart.png?version=3`. Direct asset upload is also available at Go `POST /docs/{id}/images` using field `file`, returning `{name,key,url}`. Persisted chat includes the image references.

## Chat history

Fetch `GET /chat/history?doc_id=...` when opening a document. The response is `{doc_id,messages}`, ordered oldest first. Messages include `role`, `content`, and optional `tool_calls`/`tool_call_id`. Render user and assistant entries; tool entries can populate the activity view. A missing document ID returns `400`; unavailable persistence returns `503`. Start Postgres to retain conversations across restarts.

The backend has no CORS configuration. A browser on another origin needs a same-origin proxy or a backend CORS change. The current auth model is for local development only.

## Choosing a project type

Offer `pdf` and `slideshow` when creating a project. Fetch `GET /templates?type=pdf` or `?type=slideshow` for the matching gallery. Each template includes its type. Send both the selected type and template to `POST /docs`, for example:

```json
{"type":"slideshow","template":"business-pptx","title":"Business plan"}
```

For PDF and slideshow, use `pdf_url`, `manifest_url`, and `cover_url` for the viewer. Slideshow metadata and `pdf_updated` events also contain `pptx_url` for the editable PowerPoint and additive `slides_url` scene data. The chat request shape is unchanged.

Omitting a type infers it from the template. Existing projects and old template IDs remain PDF projects. For native PowerPoint slides, use `slideshow-pptx`, `business-pptx`, or `metrics-pptx`. A mismatched type/template returns `400`.
