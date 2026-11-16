# Chat-edited PDF backend

Go owns projects, templates, PDFs, manifests, MCP tools, and WebSocket events. Rust runs the chat model/tool loop. Python renders scripts through the `Doc` wrapper. See [architecture](docs/architecture.md), [API](docs/backend-api.md), and [frontend guide](docs/frontend-guide.md).

## Run locally

Install LibreOffice Impress (the `libreoffice` executable) for slideshows. `heph run //server:run` first prepares its renderer dependencies, including `python-pptx==1.0.2`, in the interpreter configured by `server/.env` (`PDF_PYTHON`).

Install Poppler utilities (`pdftoppm` and `pdftotext`; Debian/Ubuntu package `poppler-utils`) on the server host. Covers use the first PDF page; PDF source ingestion extracts text without OCR.

Each project owns its configuration: copy `server/.env.example`, `chat/.env.example`, and `gen/.env.example` to `.env` in the same directory. Set the server data and Python paths, generator venv path, and chat model and API key. Existing local values and credentials have been preserved in those project files.

```bash
heph run //gen:venv
heph run //server:run  # separate terminal
heph run //chat:run    # separate terminal
```

Go listens on `:8080`; Rust listens on `:3000`. Go persists projects under this repo's ignored `tmp/` through `DriverFS`. Rust optionally persists chat history in Postgres. To enable chat history, run `docker compose up -d db`. Go does not need Postgres.

The Go data root defaults to repo `tmp/`. Override it with `PDF_DATA_DIR` or `-data-dir`. Each project has one `tmp/pdf/{id}/` directory containing its draft, versioned scripts, manifests, PDFs, cover images, uploaded images, SVGs, and source Markdown. Template definitions are seeded once under `tmp/templates/`; a restart never overwrites them. Clearing `tmp/` deletes projects and templates.

Each BUILD run command sources its own project’s `.env` with automatic export before starting the service. `pass_env` lists host variables allowed into the target; assignments in `.env` override those values. Go and Rust read process variables only. To select Claude, change the `envfile` dependency in `chat/BUILD` from `.env` to `.env.claude`. Chat profiles remain inside `chat/`. Real env profiles are Git ignored; each project’s `.env.example` contains no keys.

`PDF_LISTEN_ADDR`, `PDF_DATA_DIR`, `PDF_GEN_PATH`, `PDF_PYTHON`, and `AUTH_TOKEN` configure Go. An empty `PDF_GEN_PATH` lets BUILD use the staged renderer path. `PYTHON_BOOTSTRAP` and `PYTHON_VENV` configure venv creation. `CHAT_LISTEN_ADDR`, `MCP_SERVER_URL`, `DATABASE_URL`, and `RUST_LOG` configure Rust. `LLM_BASE_URL`, `LLM_MODEL`, `LLM_API_KEY`, optional `LLM_REASONING_EFFORT`, and optional `ANTHROPIC_WORKSPACE_ID` configure the OpenAI-compatible client. The client does not implement Anthropic's native Messages API.

For direct runs, export that project’s file in its shell first:

```bash
# Server terminal:
cd server
set -a
source .env
set +a
export PDF_GEN_PATH="${PDF_GEN_PATH:-../gen/run.py}"
go run ./cmd/server

# Chat terminal, starting from the repository root:
cd chat
set -a
source .env
set +a
cargo run
```

## Start a project

```bash
curl http://localhost:8080/templates
curl -s -X POST http://localhost:8080/docs \
  -H 'Content-Type: application/json' \
  -d '{"template":"report","title":"My report"}'
curl http://localhost:8080/docs
```

`POST /docs` renders version 1 before returning. Its response includes `doc_id`, `pdf_url`, `cover_url`, and `manifest_url`. The frontend opens those URLs, subscribes to `/ws`, and sends edits to Rust `POST /chat/stream`. The model saves a draft with `edit_script` and commits it with `render`.

The gallery includes `business` (business strategy slides) and `metrics` (an illustrative metrics review), alongside `empty`, `report`, and `slideshow`.

## PDF and slideshow projects

`POST /docs` accepts `type: "pdf"` or `type: "slideshow"`. PDF projects use ReportLab. Slideshow projects use python-pptx, then LibreOffice creates the PDF preview. Both share the viewer, selection manifest, cover, assets, and version history. Slideshow metadata includes `pptx_url` for editable PowerPoint downloads.

For example: `{"type":"slideshow","template":"business-pptx","title":"Business plan"}`. New slideshow template IDs are `slideshow-pptx`, `business-pptx`, and `metrics-pptx`. The older template IDs and projects retain their PDF scripts. Omitting the type infers it from the template; omitting both creates a blank PDF.

## Source documents and chat attachments

Upload reference PDF or Markdown files to `POST /docs/{id}/sources` as multipart field `file`. PDFs become Markdown text for the model’s `list_sources` and `get_source` tools. Text extraction does not preserve arbitrary PDF layouts; scanned PDFs need OCR before upload.

Chat accepts JSON or multipart fields `message`, `doc_id`, optional `system`, `selection`/`selections` JSON, and repeated `images` files. Uploaded PNG/JPEG files become document assets that the model can insert with `doc.image(name, element_id, width=None, height=None)`. Dimensions are PDF points; one dimension preserves aspect ratio. See the [frontend guide](docs/frontend-guide.md) for requests and history.

## Heph

```bash
heph run //gen:venv                   # prepare host renderer venv
heph run //gen:gen                    # sample manifest
heph run //server/cmd/server:build    # Go binary
heph run //chat:chat                  # Rust binary
heph run //:backend                   # build all targets
heph run //server:run                 # Go server, blocking
heph run //chat:run                   # Rust chat, blocking
```

Runtime and renderer targets source their project’s `.env` from their BUILD dependency and run uncached. Binary build targets do not depend on `.env`. `PDF_DATA_DIR` and `PYTHON_VENV` should be absolute host paths so data and the venv persist outside Heph's copied inputs. Server and generator Python paths are configured independently; point them to the same venv when sharing one. No checkout or home paths are hardcoded in BUILD. Use separate terminals for the two blocking server targets.

## Current limits

The Python runner has a timeout and reduced environment but no public-use sandbox. REST has only an optional shared `AUTH_TOKEN`; MCP, chat, and WebSocket have no per-user auth. The filesystem store coordinates writes within one Go process; do not run multiple Go replicas against the same data root.

## Slideshow verification

With `PDF_PYTHON` exported from the server configuration and LibreOffice installed, run `go test -v ./internal/service -run TestRealSlideshowProjectLifecycle` from `server/`. This verifies a real slideshow project, PPTX download, PDF preview, cover, manifest, and rebuilding a missing historical deck. Environments that prohibit Unix socket binding cannot start LibreOffice; this integration test reports an explicit skip there. Python tests separately verify real PPTX generation and exercise conversion orchestration with a test converter.

`//server:run` depends on the uncached `//server:renderer` setup target. Setup installs `gen/requirements.txt` using the server’s own `PDF_PYTHON` and checks imports before Go starts. This is independent of `//gen:venv`, which uses `gen/.env`. The configured server interpreter must already exist; point it at a virtual environment.
