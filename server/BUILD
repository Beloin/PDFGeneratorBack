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

# Build the server binary, then run it (blocks; Ctrl-C stops it).
# The runner subprocess needs gen/run.py + doc.py and python3 in the sandbox.
target(
    name = "run",
    driver = "bash",
    cache = False,
    deps = {
        "bin": "//server/cmd/server:build",
        "runpy": file("../gen/run.py"),
        "docpy": file("../gen/doc.py"),
    },
    tools = ["//@heph/bin:python3", "//@heph/bin:bash"],
    # pyenv's python3 shim needs PYENV_ROOT to resolve the real interpreter.
    pass_env = ["PYENV_ROOT", "PYENV_VERSION"],
    # Forced venv: the renderer runs the host venv (sandbox can read host paths
    # like /usr/bin/python3, but not the workspace's own .venv).
    run = '"$SRC_BIN" -gen "$SRC_RUNPY" -python /home/beloin/.cache/pdf-editor-back/venv/bin/python3',
)
