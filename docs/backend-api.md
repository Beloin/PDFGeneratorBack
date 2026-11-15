# Backend API — contract for the frontend

Two servers. The frontend talks to both.

| Server | Addr | Purpose |
|---|---|---|
| Go main server | `:8080` | Source of truth. REST (`/docs`) + MCP (`/mcp`) + WebSocket (`/ws`). |
| Rust chat harness | `:3000` | Chat. Talks to the LLM and to the Go server over MCP. |

The frontend does **not** talk MCP directly. It uses REST (`/docs`) for reads,
the WebSocket (`/ws`) for change events, and the Rust harness (`/chat`) for the
assistant.

Rendering is **assistant-driven**: `edit_script` only saves a draft (no version
bump, no render). The assistant calls the `render` MCP tool to commit the draft
as a new version and generate the PDF synchronously. During a render the server
pushes `updating_pdf` → `pdf_updated` (or `pdf_failed`) over the WebSocket. The
frontend never asks for a render.

---

## 1. Go server — REST (`:8080`)

Auth: all `/docs` routes accept an optional `Authorization: Bearer <token>`.
Only enforced when the server is started with `AUTH_TOKEN` set. Dev default:
unset, so no header needed.

### Create a document

```bash
curl -X POST localhost:8080/docs \
  -H 'Content-Type: application/json' \
  -d '{"template":"report"}'      # or "empty"; defaults to "empty" when omitted
```

```json
201 {"doc_id":"doc-1","version":1}
```

`template` names a **server-side** script. The frontend never sends script
text. Two seeded documents are always available for testing: `1` (report) and
`2` (slideshow, 16:9).

### Error envelope

REST errors carry a machine-readable `code`:

```json
{"error":"version 1 has no rendered manifest yet; run it first","code":"NO_MANIFEST"}
```

`code` is `ERROR` when no specific code applies.

### List versions

```bash
curl localhost:8080/docs/doc-1/versions
```

```json
{"doc_id":"doc-1","versions":[{"version":1,"created_at":"..."},{"version":2,"created_at":"..."}]}
```

### Get the manifest (element list)

```bash
curl localhost:8080/docs/doc-1/manifest
```

```json
{
  "doc_id":"doc-1",
  "page_size":[595.2755905511812,841.8897637795277],
  "elements":[{"id":"title","type":"heading","page":1,"bbox":[78.0,741.89,517.28,763.89],"line":5}]
}
```

### Get the PDF

```bash
curl -o doc.pdf localhost:8080/docs/doc-1/pdf              # head version
curl -o doc.pdf "localhost:8080/docs/doc-1/pdf?version=2"  # specific version
```

Serves `application/pdf`; `?version=` optional (defaults to head).

### Restore an older version (undo)

```bash
curl -X POST localhost:8080/docs/doc-1/restore \
  -H 'Content-Type: application/json' \
  -d '{"version":1}'
```

```json
{"doc_id":"doc-1","version":3}
```

Restore copies version 1 as a new head version (3), then re-renders (debounced).

### Health

```bash
curl localhost:8080/health    # -> {"ok":true}
```

### Not on REST

- **Script text** — read/write only over MCP, never REST.
- **No run/render endpoint** — rendering is automatic after a script change.

---

## 2. Manifest schema

The manifest is the link between a clicked region and the code. The viewer
draws clickable boxes from it.

- `page_size`: `[width, height]` in PDF points (A4 = 595.28 x 841.89).
- `elements[]`: one entry per element. An element split across pages appears
  once per page, with the **same `id`**.

```json
{
  "id": "sales-table",
  "type": "table",          // heading | paragraph | table | spacer | page_break
  "page": 2,
  "bbox": [72, 310, 523, 540],
  "line": 14
}
```

`bbox` is `[x0, y0, x1, y1]` in PDF points, origin **bottom-left**. To place a
CSS overlay, flip y first: `y_css = page_height - y_pdf`, then scale by zoom.

---

## 3. WebSocket — document events (`/ws`)

Connect to `ws://host/ws` (or `wss://`). Every message is a JSON text frame
with an envelope `{"v":1,"type":"...", ...}`. Unknown `type` is ignored
(forward compatible).

### Client → server

| type | fields | meaning |
|---|---|---|
| `subscribe` | `doc_id` | start receiving events for a document |
| `unsubscribe` | `doc_id` | stop receiving events |
| `ping` | — | liveness check; server replies `pong` |

### Server → client

| type | fields | meaning |
|---|---|---|
| `subscribed` | `doc_id`, `version` | subscription accepted; current head version |
| `updating_pdf` | `doc_id` | a script change happened, render pending — show loading |
| `pdf_updated` | `doc_id`, `version`, `actor`, `pdf_url`, `manifest_url` | render done — fetch these URLs |
| `pdf_failed` | `doc_id`, `version`, `error` | render failed — keep the old PDF, show the error |
| `error` | `code`, `message`, `doc_id?` | protocol or authorization error |
| `pong` | — | reply to `ping` |

Example flow when the assistant edits:

```json
{"v":1,"type":"subscribed","doc_id":"1","version":2}
{"v":1,"type":"updating_pdf","doc_id":"1"}
{"v":1,"type":"pdf_updated","doc_id":"1","version":3,"actor":"system","pdf_url":"/docs/1/pdf?version=3","manifest_url":"/docs/1/manifest"}
```

### Client behavior

1. Connect, `subscribe` to each open document.
2. On `subscribed`: if `version` > displayed version, refetch immediately.
3. On `updating_pdf`: show a loading state.
4. On `pdf_updated`: fetch `GET /docs/{id}/manifest`, re-render viewer, set
   displayed version. **Ignore if `version <= displayedVersion`** (dedupes).
5. On `pdf_failed`: show the error, keep the current PDF.
6. On reconnect: get a fresh connection, resubscribe, refetch any doc whose
   `subscribed.version` is newer than displayed.

---

## 4. Rust chat harness (`:3000`)

### Health

```bash
curl localhost:3000/health    # -> {"ok":true}
```

### List MCP tools (for debugging)

```bash
curl localhost:3000/tools
```

```json
[{"name":"get_script","description":"Return the current script text..."}, ...]
```

### Chat (single turn)

```bash
curl -X POST localhost:3000/chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"make the title blue","system":"You edit PDF scripts."}'
```

```json
{"reply":"I'll update the title style."}
```

`selection` is optional on `/chat`. Send
`{"element_id":"slide1-break","page":1,"bbox":[78,78,882,286]}` alongside
`message` to target a selected PDF element. The bbox uses PDF points with a
bottom-left origin.

`system` is optional. The model is configured by env on the Rust server
(`LLM_BASE_URL`, `LLM_MODEL`, `LLM_API_KEY`). LLM failure returns `502`:

```json
{"error":"..."}
```

### Call an MCP tool directly (for debugging)

```bash
curl -X POST localhost:3000/call \
  -H 'Content-Type: application/json' \
  -d '{"tool":"get_script","arguments":{"doc_id":"doc-1"}}'
```

```json
{"ok":true,"content":"{\n  \"doc_id\": \"doc-1\",\n  ...\n}"}
```

`content` is a **JSON string** — parse it to read the tool result.

---

## 5. Frontend flow (current)

1. **Load** — open the seeded doc `1`, `GET /docs/1/manifest` for element
   boxes, render the PDF.
2. **Viewer** (Phase 2) — PDF.js pages, overlay boxes from the manifest.
   Flip y and scale by zoom.
3. **Select an element** — click a box, keep `{id, page, bbox, line}` in a
   small store (Zustand).
4. **Watch for changes** — hold a `/ws` subscription; show loading on
   `updating_pdf`, refetch on `pdf_updated`.
5. **Chat** (Phase 5) — post to `/chat`; the assistant edits over MCP, which
   triggers the debounced render and the WS events above.

---

## 6. Not yet implemented (frontend blockers)

| Gap | Detail |
|---|---|
| **SSE streaming on chat** | `/chat` returns one JSON object. No token-delta / tool-event stream yet (Plan 12.2). |
| **More selection context** | `/chat` accepts `selection: {element_id, page, bbox}`. Version, element text, and cropped image are not accepted yet (Plan 12.1, Phase 5). |
