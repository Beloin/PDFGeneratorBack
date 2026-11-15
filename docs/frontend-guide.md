# Frontend guide — wire to the seeded document `1`

> **Scope now:** use the seeded document `1` only. It is pre-rendered and
> always present. Do **not** implement document creation or templates yet —
> hardcode `doc_id = "1"` for now.

## Servers

| Server | Addr | Use |
|---|---|---|
| Go main server | `http://localhost:8080` | REST + WebSocket |
| Rust chat harness | `http://localhost:3000` | chat |

---

## 1. Load document `1`

### Get the manifest (element boxes)

```bash
curl http://localhost:8080/docs/1/manifest
```

```json
{
  "doc_id": "1",
  "page_size": [595.28, 841.89],
  "elements": [
    {"id":"title","type":"heading","page":1,"bbox":[78.0,741.89,517.28,763.89],"line":5},
    {"id":"intro","type":"paragraph","page":1,"bbox":[78.0,705.89,517.28,741.89],"line":7},
    {"id":"sales-table","type":"table","page":1,"bbox":[78.0,471.89,224.57,571.89],"line":16}
  ]
}
```

### Get the PDF

```bash
curl -o doc.pdf http://localhost:8080/docs/1/pdf
```

`Content-Type: application/pdf`. Feed the bytes into PDF.js. `?version=N` gets
an older version (not needed now).

`bbox` is `[x0, y0, x1, y1]` in PDF points, origin **bottom-left**. To place an
overlay: `y_css = page_height - y_pdf`, then scale by zoom.

---

## 2. Watch for changes (WebSocket)

Connect `ws://localhost:8080/ws` and subscribe:

```js
const ws = new WebSocket("ws://localhost:8080/ws");
ws.onopen = () => ws.send(JSON.stringify({ v: 1, type: "subscribe", doc_id: "1" }));
```

Server → client events for `1`:

| type | fields | what the FE does |
|---|---|---|
| `subscribed` | `doc_id`, `version` | if `version` > displayed, refetch now |
| `updating_pdf` | `doc_id` | show a loading/spinner |
| `pdf_updated` | `doc_id`, `version`, `pdf_url`, `manifest_url` | fetch `pdf_url` + `manifest_url`, re-render, update displayed version |
| `pdf_failed` | `doc_id`, `version`, `error` | show the error, keep the current PDF |

Example `pdf_updated`:

```json
{"v":1,"type":"pdf_updated","doc_id":"1","version":3,"actor":"system",
 "pdf_url":"/docs/1/pdf?version=3","manifest_url":"/docs/1/manifest"}
```

Dedupe: ignore `pdf_updated` where `version <= displayedVersion`.

On reconnect: resubscribe to `1`, then refetch if `subscribed.version` is newer.

---

## 3. Chat (edit document `1`)

Endpoint: `POST http://localhost:3000/chat`.

### Request

```json
{
  "doc_id": "1",
  "message": "hello",
  "selection": {"element_id": "slide1-break", "page": 1, "bbox": [78, 78, 882, 286]}
}
```

| Field | Required | Notes |
|---|---|---|
| `message` | yes | the user's instruction |
| `doc_id` | no | the document to edit; use `"1"` |
| `system` | no | extra system instructions; usually leave unset |
| `selection` | no | selected `{element_id, page, bbox}`; bbox uses PDF points, origin bottom-left |

The assistant receives the selection with the message and can inspect its
`element_id` through MCP. It edits the script over MCP and renders automatically.
You do not call any render endpoint.

### Response

One JSON object, **not streamed**:

```json
{ "reply": "Changed the title color to blue." }
```

`reply` is the assistant's plain-text summary.

### Errors

```json
{ "error": "..." }
```

`502 Bad Gateway` — the LLM or MCP call failed, or the loop hit its step cap.
Show `error`.

### Timing

The request can take several seconds (the harness runs a loop: model call →
tool call → result → model call …). Set a long client timeout (e.g. 60 s) and
show a "thinking" state.

While it runs, the document changes arrive on the **WebSocket**, not in the
chat response. The two are independent:

1. You `POST /chat`.
2. The Go server pushes `updating_pdf` → `pdf_updated` over `/ws`.
3. You refetch the PDF + manifest on `pdf_updated`.
4. The `POST /chat` response returns later with the `reply`.

Do not wait for the chat response to refresh the document — refresh on the
WebSocket events.

### fetch example

```js
async function ask(message, selection = null) {
  const res = await fetch("http://localhost:3000/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ doc_id: "1", message, selection }),
  });
  if (!res.ok) {
    const { error } = await res.json();
    throw new Error(error ?? `HTTP ${res.status}`);
  }
  return (await res.json()).reply;
}
```

---

## 4. The full flow, in order

1. `GET /docs/1/manifest` + `GET /docs/1/pdf` → render the viewer.
2. Open `/ws`, `subscribe` to `"1"`.
3. User sends a message: `POST /chat {doc_id:"1", message, selection}`.
4. Server pushes `updating_pdf` (spinner) → `pdf_updated` (fetch `pdf_url` +
   `manifest_url`).
5. Re-render PDF.js + overlays, update displayed version.
6. The chat `reply` arrives; show it in the chat panel.

---

## 5. Deliberately skipped for now

- **Document creation / templates** — use `"1"`.
- **Undo UI** — `POST /docs/1/restore {"version":N}` works but is optional.
- **Streaming (SSE)** — the chat reply is one object; no token deltas or
  tool-call events yet.
