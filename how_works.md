# How it works

Go owns projects, templates, PDFs, slideshow scenes, manifests, MCP tools, and WebSocket events. Rust runs the chat model/tool loop. Python renders PDF scripts through `Doc` and slideshow scripts through `Slides`. See [architecture](docs/architecture.md), [API](docs/backend-api.md), and [frontend guide](docs/frontend-guide.md).

## Run locally

Slideshow rendering uses python-pptx and LibreOffice headless. Install LibreOffice with Impress support on the host; BUILD exposes `libreoffice` and its launcher utilities as Heph tools. `heph run //server:run` exposes the installed converter and prepares Python dependencies in the interpreter configured by `server/.env` (`PDF_PYTHON`).

LibreOffice exports PDFs without registration watermarks. Each conversion uses an isolated temporary profile so parallel renders do not share a running office process.

Install Poppler utilities (`pdftoppm` and `pdftotext`; Debian/Ubuntu package `poppler-utils`) on the server host. PDF previews for both project types are converted documents; `pdftoppm` produces their default PNG covers. PDF source ingestion extracts text without OCR. Pillow cover output is supplementary and is not the default project cover.

Each project owns its configuration: copy `server/.env.example`, `chat/.env.example`, and `gen/.env.example` to `.env` in the same directory. Set the server data and Python paths, generator venv path, and chat model and API key. `LIBREOFFICE` is optional for direct runs that need to override the installed LibreOffice executable path; BUILD passes it through to the renderer.

```bash
heph run //gen:venv
heph run //server:run  # separate terminal
heph run //chat:run    # separate terminal
```

Go listens on `:8080`; Rust listens on `:3000`. Go persists projects under this repo's ignored `tmp/` through `DriverFS`. Rust optionally persists chat history in Postgres. To enable chat history, run `docker compose up -d db`. Go does not need Postgres.

The Go data root defaults to repo `tmp/`. Override it with `PDF_DATA_DIR` or `-data-dir`. Each project has one `tmp/pdf/{id}/` directory containing its draft, versioned scripts, manifests, PDFs, cover images, uploaded images, SVGs, and source Markdown. Template definitions are seeded once under `tmp/templates/`; a restart never overwrites them. Clearing `tmp/` deletes projects and templates.

Each BUILD run command sources its own project’s `.env` with automatic export before starting the service. `pass_env` lists host variables allowed into the target; assignments in `.env` override those values. Go and Rust read process variables only. Chat uses `.env.ollama` by default. To select Claude, change the `envfile` dependency in `chat/BUILD` to `.env.claude`. Chat profiles remain inside `chat/`. Real env profiles are Git ignored; each project’s `.env.example` contains no keys.

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

`POST /docs` renders version 1 before returning. PDF metadata includes `doc_id`, `pdf_url`, `cover_url`, and `manifest_url`. Slideshow metadata includes these same viewer URLs plus `pptx_url`; `slides_url` offers the canonical scene JSON as an additive API. The frontend subscribes to `/ws` and sends edits to Rust `POST /chat/stream`. The model saves a draft with `edit_script` and commits it with `render`.

The gallery includes `business` (business strategy slides) and `metrics` (an illustrative metrics review), alongside `empty`, `report`, and `slideshow`.

## PDF and slideshow projects

`POST /docs` accepts `type: "pdf"` or `type: "slideshow"`. PDF projects use ReportLab. Slideshow projects use python-pptx and LibreOffice headless to provide the same PDF viewer workflow, alongside editable PowerPoint downloads. The versioned canonical scene JSON is additive; frontend clients do not need to build a slide renderer. Both types share the existing selection manifest, assets, and version history.

For example: `{"type":"slideshow","template":"business-pptx","title":"Business plan"}`. New slideshow template IDs are `slideshow-pptx`, `business-pptx`, and `metrics-pptx`. The older template IDs and projects retain their PDF scripts. Omitting the type infers it from the template; omitting both creates a blank PDF.

## Source documents and chat attachments

Upload reference PDF or Markdown files to `POST /docs/{id}/sources` as multipart field `file`. PDFs become Markdown text for the model’s `list_sources` and `get_source` tools. Text extraction does not preserve arbitrary PDF layouts; scanned PDFs need OCR before upload.

Chat accepts JSON or multipart fields `message`, `doc_id`, optional `system`, `selection`/`selections` JSON, and repeated `images` files. Uploaded PNG/JPEG files become document assets that the model can insert with `doc.image(...)` in PDF projects or `Slides.image(...)` in slideshows. Dimensions use points; one omitted image dimension preserves aspect ratio. See the [frontend guide](docs/frontend-guide.md) for requests and history.

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

Slideshow PDF previews and PPTX files render through the Heph-provided LibreOffice tool. Run the Go service tests and Python slideshow tests with the configured `PDF_PYTHON`; `slides_url` remains available for clients that need versioned scene geometry.

`//server:run` depends on the uncached `//server:renderer` setup target. Setup installs `gen/requirements.txt` using the server’s own `PDF_PYTHON` and checks imports before Go starts. This is independent of `//gen:venv`, which uses `gen/.env`. The configured server interpreter must already exist; point it at a virtual environment.
