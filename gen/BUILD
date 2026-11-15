# Run the sample document and publish its manifest as the target output.
target(
    name = "gen",
    driver = "bash",
    deps = [
        file("run.py"),
        file("doc.py"),
        file("examples/sample.py"),
    ],
    # pyenv's python3 is a `#!/usr/bin/env bash` shim, so bash must be on PATH.
    tools = ["//@heph/bin:python3", "//@heph/bin:bash"],
    run = "python3 run.py examples/sample.py out.pdf && cp out.pdf.manifest.json $OUT",
    out = "manifest.json",
)

# Forced venv setup (side effect): creates the host venv with the renderer's
# Python deps (reportlab, svglib). The venv lives OUTSIDE the workspace because
# the sandbox can read host paths (like /usr/bin/python3) but not the workspace's
# own .venv. Run once: `heph run //gen:venv`.
target(
    name = "venv",
    driver = "bash",
    cache = False,
    deps = {"req": file("requirements.txt")},
    run = '''
/usr/bin/python3 -m venv /home/beloin/.cache/pdf-editor-back/venv
/home/beloin/.cache/pdf-editor-back/venv/bin/python3 -m pip install --disable-pip-version-check -r "$SRC_REQ"
echo ready > "$OUT"
''',
    out = "venv.ready",
)
