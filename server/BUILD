# Go plugin needs at least one build variant before `:build` resolves.
# Scope this to the Go module (the directory holding go.mod).
provider_state(
    provider = "go",
    variants = {
        "host": {
            "goos": heph.core.os(),
            "goarch": heph.core.arch(),
        },
    },
)

# Prepare the renderer using this project's configured Python environment.
target(
    name = "renderer",
    driver = "bash",
    cache = False,
    deps = {
        "envfile": file(".env"),
        "requirements": file("../gen/requirements.txt"),
    },
    tools = ["//@heph/bin:bash"],
    pass_env = ["PDF_PYTHON", "PYENV_ROOT", "PYENV_VERSION"],
    run = '''
set -e
set -a
source "$SRC_ENVFILE"
set +a
"${PDF_PYTHON:?Set PDF_PYTHON in server/.env}" -m pip install --disable-pip-version-check -r "$SRC_REQUIREMENTS"
"$PDF_PYTHON" -c 'from pptx import Presentation; import reportlab, svglib'
echo ready > "$OUT"
''',
    out = "renderer.ready",
)

# Build the server binary, then run it (blocks; Ctrl-C stops it).
# The runner subprocess needs gen/run.py + doc.py and python3 in the sandbox.
target(
    name = "run",
    driver = "bash",
    cache = False,
    deps = {
        "bin": "//server/cmd/server:build",
        "renderer": ":renderer",
        "envfile": file(".env"),
        "runpy": file("../gen/run.py"),
        "docpy": file("../gen/doc.py"),
        "slidespy": file("../gen/slides.py"),
    },
    tools = [
        "//@heph/bin:python3",
        "//@heph/bin:bash",
        "//@heph/bin:pdftoppm",
        "//@heph/bin:pdftotext",
        "//@heph/bin:libreoffice",
        # LibreOffice's launcher resolves its installation using these commands.
        "//@heph/bin:dirname",
        "//@heph/bin:basename",
        "//@heph/bin:ls",
        "//@heph/bin:sed",
        "//@heph/bin:grep",
        "//@heph/bin:uname",
    ],
    pass_env = [
        "PDF_LISTEN_ADDR",
        "PDF_DATA_DIR",
        "PDF_GEN_PATH",
        "PDF_PYTHON",
        "AUTH_TOKEN",
        "PYENV_ROOT",
        "PYENV_VERSION",
    ],
    run = '''
set -a
source "$SRC_ENVFILE"
set +a
export PDF_GEN_PATH="${PDF_GEN_PATH:-$SRC_RUNPY}"
exec "$SRC_BIN"
''',
)
