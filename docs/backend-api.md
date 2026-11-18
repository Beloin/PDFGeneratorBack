# Backend API

The browser uses Go REST and WebSocket on `:8080`, plus Rust chat on `:3000`. Rust calls Go MCP at `/mcp`. See [architecture](architecture.md) for storage and lifecycle.

## Templates and projects (Go REST)

| Method | Path | Behavior |
|---|---|---|
| `GET` | `/templates` | Return `{"templates":[{"id":"empty","name":"Blank","cover_url":"/templates/empty/cover"}, ...]}`. Bundled IDs: `empty`, `report`, `slideshow`, `business`, `metrics`. |
| `POST` | `/docs` | Accept `{"template":"report","title":"My report"}`. Template defaults to `empty`; title defaults to its display name. Render version 1 before returning `201` project metadata. |
| `GET` | `/docs` | Return `{"docs":[...]}`, newest `updated_at` first. Pending initial renders are omitted. |
| `GET` | `/docs/{id}` | Return one project's metadata. |
| `PATCH` | `/docs/{id}` | Accept `{"title":"New title"}` and return updated metadata. Trim titles; require 1–120 Unicode characters. |
| `DELETE` | `/docs/{id}` | Delete project scripts, versioned PDF/PPTX/scene outputs, manifests, covers, and assets. Return `204`. Rust chat history is retained. |
| `GET` | `/docs/{id}/pdf?version=N` | Return the converted PDF preview for a PDF or slideshow project. Version defaults to the current head. |
| `GET` | `/docs/{id}/slides?version=N` | Return the versioned slideshow scene JSON. Version defaults to the current head; project metadata pins `slides_url` to the latest successful render. |
| `GET` | `/docs/{id}/pptx?version=N` | Return the editable PowerPoint file. Version defaults to the current head; project metadata pins `pptx_url` to the latest successful render. |
| `GET` | `/templates/{id}/cover` | Return the template’s first page as `image/png`. |
| `GET` | `/docs/{id}/cover?version=N` | Return the version’s first page as `image/png`; rebuild missing output for previously rendered versions. |
| `GET` | `/docs/{id}/manifest?version=N` | Return a version's manifest. Version defaults to the head. |
| `GET` | `/slides/fonts/regular.ttf`, `/slides/fonts/bold.ttf` | Return the Vera font files for clients that choose to render the additive scene JSON. Requires the optional REST bearer token when auth is enabled. |
| `GET` | `/docs/{id}/versions` | Return version records oldest first, including scripts and output metadata. |
| `POST` | `/docs/{id}/restore` | Accept `{"version":N}`. Copy that script into the draft; call MCP `render` to commit. |
| `GET` | `/health` | Return `{"ok":true}`. |

PDF project metadata shape:

```json
{
  "doc_id": "PROJECT_ID",
  "title": "My report",
  "template": "report",
  "type": "pdf",
  "version": 1,
  "rendered_version": 1,
  "created_at": "2026-11-15T00:00:00Z",
  "updated_at": "2026-11-15T00:00:00Z",
  "pdf_url": "/docs/PROJECT_ID/pdf?version=1",
  "cover_url": "/docs/PROJECT_ID/cover?version=1",
  "manifest_url": "/docs/PROJECT_ID/manifest?version=1"
}
```

Slideshow metadata uses `type: "slideshow"` and includes `pdf_url`,
`pptx_url`, `cover_url`, and `manifest_url`, each pinned to the latest
successful render. These are the default viewer/download URLs; `slides_url` is
additive canonical scene JSON. The `manifest_url` keeps the existing element
manifest format:

```json
{
  "doc_id": "PROJECT_ID",
  "type": "slideshow",
  "version": 3,
  "rendered_version": 3,
  "pdf_url": "/docs/PROJECT_ID/pdf?version=3",
  "slides_url": "/docs/PROJECT_ID/slides?version=3",
  "pptx_url": "/docs/PROJECT_ID/pptx?version=3",
  "cover_url": "/docs/PROJECT_ID/cover?version=3",
  "manifest_url": "/docs/PROJECT_ID/manifest?version=3"
}
```

`slides_url` returns the scene representation for clients that need scene-level
data:

```json
{
  "schema_version": 1,
  "width": 960,
  "height": 540,
  "font_family": "Vera",
  "fonts": {
    "regular_url": "/slides/fonts/regular.ttf",
    "bold_url": "/slides/fonts/bold.ttf"
  },
  "slides": [{
    "page": 1,
    "background": "#FFFFFF",
    "elements": [{
      "id": "title",
      "type": "text",
      "x": 48,
      "y": 48,
      "width": 864,
      "height": 60,
      "source_line": 12,
      "text": "Quarterly review",
      "font_size": 32,
      "color": "#111111",
      "bold": true,
      "lines": [{"text":"Quarterly review","x":48,"y":72}]
    }, {
      "id": "rule",
      "type": "rect",
      "x": 48,
      "y": 128,
      "width": 864,
      "height": 2,
      "source_line": 13,
      "fill": "#336699",
      "line": null
    }, {
      "id": "chart",
      "type": "image",
      "x": 48,
      "y": 160,
      "width": 420,
      "height": 280,
      "source_line": 14,
      "asset_name": "chart.png",
      "image_url": "/docs/PROJECT_ID/images/chart.png?version=3"
    }]
  }]
}
```

Scene text `lines[].x` and `lines[].y` are absolute top-left coordinates;
`y` is a baseline. Text elements also carry `text`, `font_size`, `color`, and
`bold`. Rectangles carry `fill` and `line`. Image elements carry `asset_name`
and a version-pinned `image_url`; the matching request, such as
`/docs/PROJECT_ID/images/chart.png?version=3`. Scene coordinates are points
from the top-left. Scene element `source_line` identifies the generating
script line. Scene dimensions default to 960x540 and may be customized by the
script. The default cover for either project type is the first page of its
converted PDF, rendered as PNG by `pdftoppm` and at most 640 pixels on its
longest edge. A Pillow-generated slideshow cover is supplementary, not the
default project cover.

`version` is the script head; `rendered_version` is the latest successful render. After a failed later render, output URLs point to `rendered_version`. `POST /docs` removes the project if its first render fails and returns `500`. Invalid input returns `400`; unknown projects return `404`. REST errors use `{"error":"...","code":"ERROR"}`. A version without rendered output returns `409` with `code: "NO_MANIFEST"`. The Go REST routes check `Authorization: Bearer <token>` only when `AUTH_TOKEN` is set. MCP and WebSocket do not use that check.

If a PDF version has a manifest but its PDF object is missing, PDF GET rebuilds it from its saved script and asset snapshot. Slideshow scene and PPTX routes similarly use that version's saved script and asset snapshot when rebuilding missing output. If no manifest exists, output GET returns `404`; rebuild failure returns `500`.

### Manifest

```json
{
  "doc_id": "PROJECT_ID",
  "page_size": [595.28, 841.89],
  "elements": [
    {"id":"title","type":"heading","page":1,"bbox":[78,741.89,517.28,763.89],"line":5}
  ]
}
```

`bbox` is `[x0, y0, x1, y1]` in PDF points, origin bottom-left. Element types include `heading`, `paragraph`, `table`, `spacer`, `page_break`, `svg`, and `image`. A split element has one entry per part under the same ID.

## Go MCP (`http://localhost:8080/mcp`)

| Tool | Arguments | Behavior |
|---|---|---|
| `get_script` | `doc_id` | Return working draft, current version, and project `type` (`pdf` or `slideshow`). |
| `get_element` | `doc_id`, `element_id` | Return manifest entries and nearby script lines. |
| `edit_script` | `doc_id`, `base_version`, `script` | Save a full replacement draft; reject stale versions. |
| `render` | `doc_id` | Commit the draft as a new version and render synchronously. Return version and manifest or a traceback. |
| `editing_pdf` | `doc_id` | Send `updating_pdf` to subscribers. |
| `add_svg` | `doc_id`, `name`, `svg` | Store an SVG at the project root; reference its name with `doc.svg(name, element_id)`. Return its storage key. |
| `list_sources` | `doc_id` | List uploaded reference documents and their IDs. |
| `get_source` | `doc_id`, `source_id` | Read the reference document as Markdown text. |
| `list_versions` | `doc_id` | Return versions oldest first. |
| `restore_version` | `doc_id`, `version` | Copy an older script into the draft. |
| `list_elements` | `doc_id` | Return current head manifest. |

`render` commits before Python runs. A failed render can leave the new head version without a PDF or manifest. Project assets live at `tmp/pdf/{id}/{name}.{svg,png,jpg}`; successful renders snapshot them under `versions/{n}/assets/`. The active Go driver is `DriverFS`, rooted at repo `tmp/` by default.

## Sources and image assets (Go REST)

All routes below use the same optional REST bearer token and require an existing project. Generated storage keys remain under the project directory.

| Method | Path | Behavior |
|---|---|---|
| `POST` | `/docs/{id}/sources` | Multipart field `file`: PDF or UTF-8 Markdown. Return `201` with source metadata. PDF text is converted to `.md`. |
| `GET` | `/docs/{id}/sources` | Return `{doc_id,sources:[...]}`. Use source IDs with `get_source`. |
| `GET` | `/docs/{id}/sources/{sourceID}` | Return `text/markdown; charset=utf-8`. |
| `POST` | `/docs/{id}/images` | Multipart field `file`: PNG/JPEG. Return `201 {name,key,url}`. |
| `GET` | `/docs/{id}/images/{name}?version=N` | Return the stored PNG/JPEG, using that render's asset snapshot when a version is specified. |

Source uploads allow 20 MiB of input and at most 8 MiB of text. PDF conversion uses Poppler `pdftotext` with a 20-second timeout. No OCR is performed; PDFs without extractable text return `422`. Markdown structure and complex PDF layout are not reconstructed. Images allow up to 12 MiB and 20 million pixels through Go directly; chat applies smaller limits below. The model inserts uploaded images in PDF projects with `doc.image(name, element_id, width=None, height=None)` and in slideshows with `image(name, element_id, x, y, width=None, height=None)`. Dimensions are points; setting one dimension preserves aspect ratio.

Successful renders persist `cover_key` on each version and return `cover_url` in project metadata and version listing entries. Default PDF-page covers are 640 pixels on their longest edge. Template startup generates missing covers without replacing stored template scripts.

## Document WebSocket (`ws://localhost:8080/ws`)

Send `{"v":1,"type":"subscribe","doc_id":"PROJECT_ID"}`. `unsubscribe` accepts `doc_id`; `ping` receives `pong`.

| Event | Fields | Client action |
|---|---|---|
| `subscribed` | `doc_id`, `version` | Refetch if newer than displayed version. |
| `updating_pdf` | `doc_id` | Show loading. |
| `pdf_updated` | PDF fields remain `doc_id`, `version`, `actor`, `pdf_url`, `manifest_url`, `cover_url`. Slideshow adds `project_type: "slideshow"`, `slides_url`, and `pptx_url`, retaining the PDF, manifest, and cover URLs. | Refetch the PDF viewer outputs and refresh the PPTX download. The event name remains `pdf_updated`. |
| `pdf_failed` | `doc_id`, `version`, `error` | Show error; keep last usable PDF. |
| `error` | `code`, `message`, optional `doc_id` | Handle protocol error. |

Events are in-process hints. Resubscribe and refetch after reconnecting. The current server allows all subscriptions in development.

## Rust chat (`http://localhost:3000`)

`GET /health` returns `{"ok":true}`. `GET /tools` lists Go MCP tools. `POST /call` is a debug MCP proxy. `POST /chat` accepts `message`, optional `doc_id`, `system`, and `selections: [{element_id, page, bbox}]`. The legacy `selection: {element_id, page, bbox}` remains supported and is merged with the array. It returns one JSON object, `{"reply":"..."}`, or `502 {"error":"..."}`. `POST /chat/stream` accepts the same JSON and returns SSE events: `message` with `{delta}`, `tool_call` with `{id,name,arguments}`, `tool_result` with `{id,name,content}`, then `done` with `{reply}`. Failures after the stream begins use `error` with `{error}`. `GET /chat/history?doc_id=...` returns `{doc_id,messages}` oldest first when Postgres is available; it returns `400` without a document ID and `503` when persistence is unavailable. Chat has no per-user auth.

Both chat POST routes also accept `multipart/form-data`: text fields `message`, `doc_id`, optional `system`, JSON text fields `selection` and `selections`, and repeated file fields `images`. Images require a document ID. Limits are 20 MiB total, eight images, and 5 MiB per image. Rust uploads images to Go before starting the model loop or SSE response. Configure Rust `AUTH_TOKEN` to match Go when REST authentication is enabled. JSON requests continue to work. Selection context and uploaded `{name,key,url}` references are included in persisted user content.

## Project types and PowerPoint output

`POST /docs` accepts optional `type`, either `pdf` or `slideshow`. A selected template determines the type when omitted. Without a template, PDF defaults to `empty` and slideshow defaults to `slideshow-pptx`. An unknown type or a type/template mismatch returns `400`. Existing records without a type are read as `pdf`.

`GET /templates?type=slideshow` filters the gallery. Each template includes `type`; the new slideshow IDs are `slideshow-pptx`, `business-pptx`, and `metrics-pptx`. Existing `slideshow`, `business`, and `metrics` templates remain PDF templates for compatibility.

Project metadata includes `type`. Slideshow projects include `pdf_url`, `pptx_url`, `cover_url`, and `manifest_url`, pinned to the latest successful render. `pdf_url` is the default viewer preview; `pptx_url` returns an editable PowerPoint download. `slides_url` remains available for clients that need the additive canonical scene JSON. Historical slide images in that scene are available from the project's version snapshot through the `version` query parameter.

MCP `get_script` includes `type`, so the model chooses `Doc` for PDF or `Slides` for slideshow. `Slides` uses points from the top-left for scene coordinates; the existing manifest continues to use bottom-left PDF coordinates. Stable IDs cover text boxes, rectangles, and raster images. The backend converts the saved PPTX into the PDF preview with LibreOffice headless. Scene and Vera font endpoints are optional for clients that need scene-level data; the default frontend continues to use the PDF and manifest.

`pdf_updated` events keep `type: "pdf_updated"`. PDF event fields are unchanged. Slideshow events add `project_type: "slideshow"`, `slides_url`, and `pptx_url` while retaining the version's `pdf_url`, `cover_url`, and `manifest_url`.

Example slideshow update event:

```json
{
  "v": 1,
  "type": "pdf_updated",
  "doc_id": "PROJECT_ID",
  "version": 3,
  "actor": "system",
  "pdf_url": "/docs/PROJECT_ID/pdf?version=3",
  "cover_url": "/docs/PROJECT_ID/cover?version=3",
  "manifest_url": "/docs/PROJECT_ID/manifest?version=3",
  "project_type": "slideshow",
  "pptx_url": "/docs/PROJECT_ID/pptx?version=3",
  "slides_url": "/docs/PROJECT_ID/slides?version=3"
}
```
