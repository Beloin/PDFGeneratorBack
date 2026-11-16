# Design plan: Chat-Edited PDF Generator

This is the original design proposal and decision log. Sections below describe planned behavior, including features that are not implemented. For the current system, use [architecture](architecture.md), [API contract](backend-api.md), and [frontend guide](frontend-guide.md).

## Current implementation snapshot

- Go owns REST, MCP, WebSocket events, versioned scripts, drafts, PDF rendering, and manifests. It uses `mark3labs/mcp-go` and a key-based `DriverFS` store under repo `tmp/`. A future driver can use S3 or GCP.
- Rust uses `rmcp` and an OpenAI-compatible client for a working model/tool loop. It can persist chat in Postgres. `/chat` returns one JSON object; SSE, token streaming, auth forwarding, and per-user isolation are still proposed.
- Python uses ReportLab and `svglib` through `Doc`. It runs as a local subprocess with a timeout and reduced environment; a public-use sandbox is still proposed.
- The browser frontend is outside this repository. Go sends PDF updates over WebSocket. The chat request accepts `selection: {element_id, page, bbox}`; element text and cropped images are still proposed.
- Template files seed once under `tmp/templates/`; projects are created on demand under `tmp/pdf/{id}/`. Each project keeps scripts, manifests, PDFs, and SVGs together. Rust alone uses Postgres for optional chat history.

The original phase checklist and examples below remain as planning history. They are not acceptance claims for the current code.

## 1. Goal

A web app where a user views a generated PDF, **selects a part of it** (an element, a region, a page), and **asks a chat assistant to change it**. The assistant edits the Python script that generates the PDF, re-runs it, and the viewer refreshes.

There is **no manual editing UI**. All edits go through the chat.

## 2. Architecture

```
Browser (PDF viewer + clickable overlays + chat panel)
   │  (REST + SSE)
   ▼
Chat server (Rust)  ── harness: LLM loop, history, streaming
   │  (MCP client over Streamable HTTP, user token forwarded)
   ▼
Main server (Go)    ── MCP server + REST API, owns scripts/PDFs/manifests, authz
   │  (subprocess)
   ▼
Python runner       ── executes the generated script, writes out.pdf + manifest
```

- **Go main server**: source of truth for documents, script versions, and rendered outputs. Exposes the tools as MCP and the same operations as REST for the UI.
- **Rust chat server (the "harness")**: runs the model loop (model call → tool calls → results → repeat), streams to the browser over SSE, stores chat history. Stateless apart from the DB, so it can scale horizontally.
- **Python runner**: runs the AI-written script. **Initially runs locally as a subprocess on the server** (see Risks).

## 3. Key design decision: selection → code mapping

A PDF does not remember the code that made it. We record the link **at render time**:

1. The AI must write scripts using a thin wrapper library (`Doc`) where every element has a **mandatory, stable `id`**.
2. On every render, the wrapper writes `out.pdf.manifest.json` listing each element's id, page, bounding box, and source line.
3. The viewer overlays clickable boxes from the manifest. Clicking one selects that `id`.
4. The selection (id, page, bbox, element text, optional cropped image) is sent with the chat message. The model finds the call with that `id` in the script and edits it.

Manifest format:

```json
{
  "page_size": [595.27, 841.89],
  "elements": [
    {
      "id": "sales-table",
      "type": "table",
      "page": 2,
      "bbox": [72, 310, 523, 540],
      "line": 14,
      "text": "Region | Revenue | ..."
    }
  ]
}
```

Rules:
- bbox is in PDF points, origin **bottom-left**. The viewer flips y: `y_css = page_height - y_pdf`, then scales to zoom.
- An element split across pages appears as multiple entries with the **same id**.
- IDs are stable across edits. The model must never rename or drop existing IDs unless asked.

## 4. Tech choices

| Area | Choice | Notes |
|---|---|---|
| PDF generation | ReportLab (Platypus) | Free, fast, pure Python |
| PDF viewing | PDF.js | Render pages to canvas, overlay absolutely positioned divs |
| Main server | Go | MCP via the official Go SDK (`modelcontextprotocol/go-sdk`) or `mark3labs/mcp-go`; verify current status before choosing |
| Chat server | Rust (axum) | MCP client via `rmcp`; LLM calls via `genai` or a provider crate; decide whether to hand-roll the loop (preferred first) or use `rig` |
| DB | SQLite to start, Postgres later | Documents, script versions, chat messages |
| Frontend | React | PDF.js for the viewer and overlays, **assistant-ui** for the chat panel (custom adapter to the Rust SSE endpoint), a small store (e.g. Zustand) for shared selection state |

## 5. Tools exposed by the Go server (MCP + REST)

- `get_script(doc_id)` → current script text and version
- `get_element(doc_id, element_id)` → manifest entry plus surrounding script lines
- `edit_script(doc_id, base_version, patch)` → new version (reject if `base_version` is stale)
- `run_script(doc_id, version)` → `{ok, traceback?, pdf_url, manifest}`
- `render_page(doc_id, page)` → PNG, for the model's visual checks (later phase)
- `list_versions(doc_id)` / `restore_version(doc_id, version)` → undo

Every call authorizes the **end user** (forwarded token) against the document owner. Never trust `user_id` from the model.

## 6. Phases

### Phase 0: Repo skeleton
- Monorepo: `/gen` (Python wrapper + runner), `/server` (Go), `/chat` (Rust), `/web`.
- A top-level `README` explaining how to run each piece locally.
- **Done when**: each directory builds or runs a hello-world.

### Phase 1: Python wrapper + manifest (no AI)
- Implement `Doc` with `heading`, `paragraph`, `image`, `table`, `spacer`, `page_break`, each requiring `id`.
- Implement the tagging mechanism (a wrapper flowable that records bbox on `draw()` and handles `split()`).
- CLI: `python run.py script.py out.pdf` → writes the PDF and the manifest.
- Reject duplicate IDs with a clear error message.
- **Done when**: a hand-written script with a long paragraph that spans two pages and a table yields a correct manifest, and the bboxes are verified by drawing them on a rasterized page.

### Phase 2: Viewer with selectable regions (no AI)
- PDF.js page rendering, overlay boxes from the manifest, click to select, highlight selected, show the selected id.
- Optional: drag-select a rectangle and intersect with element bboxes.
- **Done when**: clicking any element in a sample PDF shows the right id on every page and at multiple zoom levels.

### Phase 3: Go server
- Storage: documents, script versions, outputs.
- Subprocess runner behind a single `run_script` function: **timeout, temp working dir, scrubbed environment, no secrets passed**, capture stderr for tracebacks.
- REST for the UI, then the same operations as MCP tools (section 5).
- Auth (token-based) on every route and tool.
- **Done when**: you can create a doc, edit the script via API, re-run it, get a new PDF + manifest, and restore an older version.

### Phase 4: Rust chat harness
- `POST /chat` (SSE streaming): loads history, connects to the Go MCP server (forwarding the user token), merges tool schemas, runs the loop with a **max-step cap, token budget, and timeouts**.
- Persist messages including tool calls and results.
- System prompt rules: always use the `Doc` wrapper; every element needs a unique stable `id`; keep existing IDs; change only the selected element unless told otherwise; run `run_script` after editing and fix errors from the traceback.
- **Done when**: a plain-text request ("make the title blue") edits the script and returns a new PDF version, with no UI selection yet.

### Phase 5: Selection-aware chat in the UI
- Chat panel built with **assistant-ui** (React). Write a small adapter that posts to the Rust `/chat` endpoint and maps our SSE events (token deltas, tool call started, tool result, document updated) to assistant-ui's message and tool-call model. Check the current assistant-ui docs for the runtime or adapter API before starting.
- Render tool calls as UI (for example "Editing sales-table", a spinner while `run_script` runs, and the traceback on errors).
- Customize the composer to show a selection chip ("Editing: sales-table, p.2") with a way to clear it.
- Keep the selection in a small shared store (e.g. Zustand) used by both the PDF viewer and the chat panel.
- On the "document updated" event, refetch the PDF and manifest and re-render the viewer.
- Chat panel with a selection chip ("Editing: sales-table, p.2").
- Chat request includes `doc_id`, `version`, selected `element_id`s, page, bbox, element text, optional cropped image.
- The harness gives the model a compact view (element list + the selected element's script lines), not the whole script by default.
- After a successful run, the UI refreshes the PDF and manifest.
- **Done when**: select an element → "make this bigger / change this wording" edits only that element.

### Phase 6: Hardening and polish
> **Sandboxing, OAuth, and quotas are blockers for exposing the MCP publicly (see section 10).** Do them before any public launch.

- Sandboxed execution (container with no network, read-only filesystem except the output dir, CPU/memory/time limits, pinned libraries).
- Undo/redo UI over script versions.
- `render_page` tool for visual verification (overflow, overlap).
- Proposals/diff view before applying large changes (optional).
- Conflict handling: version check on `edit_script`; the harness reloads and retries or stops.
- Rate limits and per-user quotas.

## 7. Conventions

- Keep the wrapper API **small and strict**; fewer methods means fewer ways for the model to produce unmappable output.
- Script edits should be **minimal patches**, not whole-file rewrites.
- All elements drawn directly on the canvas (headers, footers, backgrounds) should either get wrapper methods or be explicitly non-selectable.
- Errors from `run_script` must be returned to the model verbatim (traceback), so it can self-correct.
- Never pass environment variables or secrets into the runner.

## 8. Risks and mitigations

| Risk | Mitigation |
|---|---|
| AI-written code runs on the server | Phase 3: subprocess with timeout, temp dir, scrubbed env. Phase 6: real sandbox. Do not expose to other users before Phase 6. |
| Model drops or renames IDs | Validate after each run: compare manifest IDs before/after; warn or retry if existing IDs vanished unexpectedly. |
| Split elements (tables/paragraphs across pages) | Same id on every part; test in Phase 1. |
| Stale edits (concurrent changes) | Versioned `edit_script` with `base_version`. |
| Prompt injection via document text | Treat element text as untrusted data; authorization lives in the Go tools, not the prompt. |
| Rust LLM ecosystem is young | Hand-roll the loop on a thin client; keep the loop behind a small interface. |

## 9. Open questions (decide before or during the phases noted)

- Build tooling for the React app (Vite is a reasonable default) and styling approach (before Phase 2).
- Which LLM provider and model (before Phase 4).
- Which Go MCP SDK to use (before Phase 3).
- SQLite vs Postgres from the start (before Phase 3).
- Whether the model should see a rendered image of the selection by default or only on request (Phase 5).

## 10. Public MCP for external chatbots

The Go server's MCP endpoint must be reachable by **other chatbots** (third-party MCP clients), not only our own Rust harness, so they can create and edit PDFs. This changes the priorities above.

**Hard prerequisites before exposing it publicly** (pull these forward from Phase 6):
- **Real sandboxing of the Python runner.** External models will submit code to be executed. A local subprocess is not acceptable once the endpoint is public.
- **Proper auth** (below), per-user data isolation, and quotas.

**Transport and auth**
- Streamable HTTP over HTTPS at a stable URL.
- OAuth 2.1 following the MCP authorization spec (PKCE, per-user access tokens, scopes such as `pdf:read` and `pdf:write`). Check the current spec for client registration details (dynamic client registration or client ID metadata documents) before implementing.
- API keys are acceptable for early development only; do not use one shared key for multiple users.
- Our own Rust harness can keep using the same MCP surface with its own internal auth path or the same OAuth flow.

**Tool surface**
- Same tools as section 5, with **good tool descriptions**, since an external model only knows what the server tells it. Also publish an MCP resource documenting the `Doc` wrapper API and its rules (mandatory unique ids, keep existing ids, minimal patches).
- Mark tools with annotations (read-only vs destructive) where the SDK supports it.
- Strongly consider a second, safer creation path: `create_pdf_from_spec(spec)` taking a **declarative JSON document spec** (elements, ids, styles) rendered by the same wrapper without running model-written code. Keep the raw-script tools for advanced use, behind the sandbox and stricter quotas.
- External clients have no viewer or click-selection, so add `list_elements(doc_id)` (the manifest) so they can target elements by id and text.

**Outputs**
- Return **signed, expiring download URLs** for PDFs rather than base64 blobs. Return the manifest inline (it is small).

**Abuse and cost controls**
- Per-user rate limits and quotas, max script size, max pages, max runtime and memory per run, max output size, storage TTL and per-user storage cap.
- All documents scoped to the owning user; document ids unguessable; authorize on every call.
- Treat all tool inputs from external clients as untrusted.

**Open questions**
- Which authorization server to use (build into the Go server vs an external identity provider).
- Free vs paid usage, and how quotas are enforced.
- Whether to ship the JSON-spec path first and defer raw scripts for external clients.

## 11. Decision log (what we considered and why)

### 11.1 Harness: drop-in vs DIY
- **Terminology**: the "harness" is the code around the model that runs the loop (model call → tool calls → results → repeat), manages context and history, streams output, and enforces limits. "Agent runtime" and "agent loop" mean roughly the same thing.
- **An agent SDK is a loop runner, not a router.** A router or gateway (OpenRouter, LiteLLM) chooses which model or provider handles a request and does not run tools. An agent SDK runs the tool loop.
- **Drop-in route**: prebuilt chat UI (assistant-ui, CopilotKit, Vercel AI SDK `useChat`) plus an agent SDK or framework, with tools as function tools or MCP. Provider APIs can also call a *remote* MCP server directly, but that needs a publicly reachable HTTPS endpoint. Fastest to ship; less control over the loop and some vendor coupling.
- **DIY route**: own the loop on our own server. The loop itself is roughly 50-150 lines in any language. **Chosen: DIY**, in Rust.
- **Tools reach the loop in two ways** that look identical to the model: in-process functions, or MCP tools via an MCP client. We use **MCP**, because the tools live in a separate Go server and must also be usable by external chatbots (section 10).

### 11.2 Topology
- Options considered: (A) single process with loop and tools together; (B) separate harness service calling tools over MCP; (C) harness that is both MCP server and client.
- **Chosen: B.** Rust chat server calls the Go main server, which owns the MCP tools and the data. Isolation between servers was a stated requirement.

### 11.3 Loop as a plain process vs a custom container
- **Decision: the loop runs as a normal process inside the Rust chat server.** It only does I/O (LLM API plus MCP calls), has no privileges of its own, and its blast radius is bounded by the Go tools' authorization. Containers per session add cold starts, orchestration, and state hand-off for little security gain.
- **Exception, and it now applies**: running AI-written Python *is* the case that needs isolation. Only the code execution goes into a sandbox; the loop stays a process. Locally at first (subprocess), then a real sandbox before public exposure.
- Revisit per-session sandboxes later if we add file handling, browsing, or long-running autonomous tasks.

### 11.4 Libraries considered

| Library | Language | Verdict |
|---|---|---|
| Vercel AI SDK | TS | Best fit if the chat server were TS: loop, streaming, MCP client, `useChat` |
| Mastra | TS | Fuller agent framework on top of the AI SDK |
| LangGraph | Python, TS only | Explicit state graphs, checkpointing, human-in-the-loop. Heavier than needed for a tool loop and **unusable from Rust**. Revisit only if flows grow into branching, durable, approval-heavy workflows |
| Pydantic AI | Python | Lightweight typed agents with MCP support |
| OpenAI Agents SDK | Python | Agents, tools, sessions, MCP; shaped around OpenAI |
| Claude Agent SDK | TS, Python | Built on the Claude Code harness; geared to agents with their own working environment. Heavier than needed for a multi-tenant chat box |
| Spring AI, LangChain4j | JVM | Good if the chat server were JVM |
| **rmcp** | Rust | Official Rust MCP SDK; use as the MCP client |
| **Rig (rig-core)** | Rust | Most complete Rust agent framework; MCP via rmcp |
| **genai** or provider crates | Rust | Thin multi-provider clients; we write the loop |

**Decision**: Rust chat server with `rmcp` plus a thin LLM client, hand-rolling the loop first and keeping it behind a small interface. Try Rig if hand-rolling becomes a burden. The Rust ecosystem is young, so verify each crate's current state before committing. History persistence and auth forwarding are ours to build regardless of library.

### 11.5 Editing model: what we dropped
- We first considered a **structured document model** (JSON tree of pages and blocks with stable ids, an operations API such as `update_block` and `set_style`, an HTML/CSS preview, and manual editing in the UI alongside the chat).
- **Dropped**, because the AI generates the PDF with a Python script and there is no manual editing. Keeping both a script and a manual editor in sync would be painful.
- **Ideas we kept**: stable element ids, versioning with undo, passing selection context to the chat, compact views instead of sending the whole document, optional proposal/diff before applying large changes, and live refresh of the viewer after edits.

### 11.6 PDF generation and selection mapping
- **Chosen: ReportLab (Platypus)** for being free, fast, and pure Python, plus **PDF.js** for viewing.
- **Alternatives**: HTML → PDF via WeasyPrint or headless Chromium (selection becomes easy with `data-id` attributes on the HTML preview, and the PDF is only an export, but it is slower and heavier); Typst; react-pdf.
- **Fallback if scripts are not instrumented**: extract text with bounding boxes (PyMuPDF or pdfplumber) and fuzzy-match strings back to the script. Fragile for generated values, tables, images, and repeated text. Not the plan.
- **Why IDs over line numbers**: lines shift after every edit; ids are stable.

### 11.7 Execution environment
- **Now**: planning only, and everything runs locally on the server. The script runs as a separate subprocess with a timeout, a temp working directory, and no environment variables or secrets passed in. All execution goes through one `run_script` function so the sandbox can be swapped in later in one place.
- **Later (Phase 6, and a blocker for public MCP)**: throwaway container, for example `docker run --rm --network none --read-only --memory 256m --cpus 1` with only an output directory writable, pinned libraries, and a hard timeout. Move to gVisor or Firecracker for stronger isolation if needed.

### 11.8 Chat panel library
- **Chosen: assistant-ui on React.** It is backend-agnostic (we write an adapter to our own SSE events, so the Rust harness does not have to imitate another vendor's stream protocol), renders tool calls as UI, supports a customizable composer (needed for the selection chip), and handles message list, streaming text, markdown, retry, and cancel.
- **Alternative: Vercel AI SDK `useChat`.** Also works with Vue and Svelte, but expects its own stream protocol, so the Rust server would have to emit that format or use a custom transport. Its stream spec has changed between versions.
- **Skipped**: CopilotKit (heavier, own protocol and runtime), plain chat UI kits (visual only; we would build streaming and tool-call handling ourselves), and a fully hand-rolled panel (fine for a throwaway prototype, but we would rebuild what assistant-ui provides).
- Both libraries' APIs change fairly often, so verify the current setup docs before implementing.

## 12. Harness design details

### 12.1 The loop
1. Receive `POST /chat` with the user's token, `doc_id`, `version`, message, and selection context.
2. Load conversation history.
3. Connect to the Go MCP server (forwarding the user token), list its tools, and merge their schemas with any local tools.
4. Call the model with messages and tool schemas.
5. If the response contains tool calls, execute each over MCP, append the results, and call the model again.
6. Stop on plain text, on the step cap, or on a timeout or budget limit.
7. Persist the turn (messages, tool calls, results) and stream events to the browser.

### 12.2 Streaming events to the UI (SSE)
- Token deltas, tool call started, tool result, and **document updated** (carrying the new version, so the viewer refetches the PDF and manifest).
- Start simple (events on the chat stream). If the user could trigger changes elsewhere, move to a separate push channel (WebSocket or SSE) per open document.

### 12.3 Context management
- Give the model a **compact view**: the element list (ids and short summaries) and the selected element's script lines. It fetches more with `get_script` or `get_element` when needed. Do not paste the whole script by default.
- System prompt rules: always use the `Doc` wrapper; every element has a unique stable `id`; keep existing ids; change only the selected element unless asked; after editing, call `run_script` and fix errors from the traceback.
- Long histories: truncate or summarize older turns; this is ours to build.

### 12.4 Identity propagation (chat server to Go server)
- **Preferred**: forward the user's access token on every MCP request (the HTTP transport supports auth headers) and have the Go server authorize against that user.
- **Alternative**: a service credential plus a signed "acting as user X" claim that the Go server verifies.
- **Never** give the chat server a god-mode token. A prompt injection could then act on any user's documents.
- Keep the Go MCP endpoint on a private network or behind auth when serving only our own harness. The public deployment (section 10) uses OAuth.

### 12.5 Security checklist
- **Authorization lives in the tools, not the prompt.** The model decides what to call; the Go server decides whether it is allowed. Derive identity from the token, never from model-supplied arguments such as `user_id`.
- Validate all tool arguments server-side; make tools idempotent where possible.
- Treat document text and tool results as untrusted input (prompt injection through content).
- Confirm or preview destructive or large changes; at minimum keep versions so everything is undoable.
- Cap steps, tokens, run time, and per-user usage. Log every tool call with user, arguments, and result.
- Per-tenant separation in the chat server: no shared mutable globals across requests.

### 12.6 Persistence
- Messages table: conversation id, role, content blocks, tool calls and results, timestamp, and the document version each turn started from.
- Script versions table (Go side): document id, version number, script text, created by (user or assistant), and the rendered output and manifest references.

### 12.7 Conflict handling
- `edit_script` takes `base_version`. If the document has moved on, the call fails; the harness reloads the relevant element and retries, or stops and tells the user.

## 13. Glossary

- **Harness**: the loop and supporting code that connects a model to tools, context, history, and streaming.
- **MCP (Model Context Protocol)**: the protocol through which a client discovers and calls tools on a server. Here the Go server is the MCP server; the Rust harness and external chatbots are clients.
- **Manifest**: the JSON file written on each render that maps element ids to page, bounding box, and source line.
- **bbox**: bounding box in PDF points, origin bottom-left.
- **Element id**: the stable, unique name every element must have in the script; the link between what the user clicks and the code to edit.

## Appendix: wrapper sketch (ReportLab)

Starting point for Phase 1; verify with long paragraphs and tables.

```python
import inspect, json
from reportlab.platypus import BaseDocTemplate, Frame, PageTemplate, Paragraph, Flowable
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import getSampleStyleSheet

class Tagged(Flowable):
    def __init__(self, inner, id, registry, line):
        super().__init__()
        self.inner, self.id, self.registry, self.line = inner, id, registry, line

    def wrap(self, aw, ah):
        self.width, self.height = self.inner.wrap(aw, ah)
        return self.width, self.height

    def split(self, aw, ah):
        return [Tagged(p, self.id, self.registry, self.line)
                for p in self.inner.split(aw, ah)]

    def draw(self):
        x, y = self.canv.absolutePosition(0, 0)
        self.registry.append({
            "id": self.id,
            "page": self.canv.getPageNumber(),
            "bbox": [x, y, x + self.width, y + self.height],
            "line": self.line,
        })
        self.inner.drawOn(self.canv, 0, 0)

class Doc:
    def __init__(self, path):
        self.path, self.story, self.registry = path, [], []
        self.styles = getSampleStyleSheet()

    def _add(self, flowable, id):
        line = inspect.stack()[2].lineno
        self.story.append(Tagged(flowable, id, self.registry, line))

    def heading(self, text, id):
        self._add(Paragraph(text, self.styles["Heading1"]), id)

    def paragraph(self, text, id):
        self._add(Paragraph(text, self.styles["BodyText"]), id)

    def save(self):
        doc = BaseDocTemplate(self.path, pagesize=A4)
        frame = Frame(doc.leftMargin, doc.bottomMargin, doc.width, doc.height)
        doc.addPageTemplates([PageTemplate(id="main", frames=frame)])
        doc.build(self.story)
        with open(self.path + ".manifest.json", "w") as f:
            json.dump({"page_size": list(A4), "elements": self.registry}, f)
```
