# Run the sample document and publish its manifest as the target output.
target(
    name = "gen",
    cache = False,
    driver = "bash",
    deps = [
        file(".env"),
        file("run.py"),
        file("doc.py"),
        file("examples/sample.py"),
    ],
    tools = ["//@heph/bin:python3", "//@heph/bin:bash"],
    pass_env = ["PDF_PYTHON", "PYENV_ROOT", "PYENV_VERSION"],
    run = '''
set -a
source ./.env
set +a
"${PDF_PYTHON:?Set PDF_PYTHON in .env}" run.py examples/sample.py out.pdf
cp out.pdf.manifest.json "$OUT"
''',
    out = "manifest.json",
)

# Creates the renderer venv at the host path configured in .env.
target(
    name = "venv",
    driver = "bash",
    cache = False,
    deps = {
        "req": file("requirements.txt"),
        "envfile": file(".env"),
    },
    pass_env = ["PYTHON_BOOTSTRAP", "PYTHON_VENV"],
    run = '''
set -a
source "$SRC_ENVFILE"
set +a
"${PYTHON_BOOTSTRAP:?Set PYTHON_BOOTSTRAP in .env}" -m venv "${PYTHON_VENV:?Set PYTHON_VENV in .env}"
"$PYTHON_VENV/bin/python3" -m pip install --disable-pip-version-check -r "$SRC_REQ"
echo ready > "$OUT"
''',
    out = "venv.ready",
)
