# Chat-Edited PDF Generator — backend

Monorepo for the backend of the chat-edited PDF generator. See `docs/PLAN.md`
for the full plan.

## Services

| Directory | Language | Role |
|---|---|---|
| `server/` | Go | Main server: source of truth for documents, script versions, and rendered outputs. Exposes the tools as MCP (streamable HTTP at `/mcp`) and the same operations as REST (`/docs`). |
| `chat/` | Rust | Chat harness: connects to the Go MCP server as an MCP client (rmcp) and exposes the tools over HTTP. The LLM loop is Phase 4. |
| `gen/` | Python | Runner + `Doc` wrapper. Runs an AI-written script and writes `out.pdf` + `out.pdf.manifest.json`. |

`web/` (the React frontend) is out of scope for this backend repo.

## Tools (MCP + REST)

`get_script`, `get_element`, `edit_script`, `run_script`, `list_versions`,
`restore_version`, `list_elements`.

## Run locally

```bash
# Go server (:8080)
(cd server && go run ./cmd/server -gen ../gen/run.py)

# Rust chat harness (:3000)
(cd chat && cargo run)          # MCP_SERVER_URL defaults to http://localhost:8080/mcp
```

The Rust server loads `chat/.env` for its LLM settings. To use the Claude profile,
run `(cd chat && CHAT_ENV_FILE=.env.claude cargo run)`. Both files are ignored by
Git; shell environment variables take precedence. Unscoped Claude keys also
need `ANTHROPIC_WORKSPACE_ID` in `chat/.env.claude`.

With heph, run `heph r //chat:run` for `chat/.env`, or
`CHAT_ENV_FILE=.env.claude heph r //chat:run` for the Claude profile. The Rust
process reads these files from the workspace; they are not copied into the
heph sandbox.

REST (Go server):

```bash
curl -s localhost:8080/health
curl -s localhost:3000/health          # Rust harness
curl -s localhost:3000/tools           # list MCP tools (Rust -> Go)
```

## Build with heph

```bash
heph run //gen:gen                    # run the sample PDF, cat the manifest
heph run //server/cmd/server:build    # Go binary
heph run //chat:chat                  # Rust binary
heph run //:backend                   # all three
```

`//server:run` keeps PDFs in `tmp/pdfs/` and assets in `tmp/assets/` at the repo
root. Both are Git ignored. Use `-out` and `-assets` when running the Go binary
directly to choose other locations.

## Phase status

Phase 0 done. Phase 1 (`Doc` wrapper + manifest) done: multi-page split keeps
the same id. Phase 3 core done: documents, versioned edits with stale-reject,
subprocess `run_script` (timeout + scrubbed env), REST + MCP tools, in-memory
store. Phase 4 partially: MCP client wiring works (`/tools`, `/call`); the model
loop and auth forwarding are still to come. Phase 6 (sandbox, OAuth, quotas) not
started.
