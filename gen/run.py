#!/usr/bin/env python3
"""Run an AI-written Doc script and produce out.pdf + out.pdf.manifest.json.

Usage: python run.py script.py out.pdf

The script runs in a namespace with OUT_PATH set; it should do:

    from doc import Doc
    doc = Doc(OUT_PATH)
    ... doc.heading(...) / doc.paragraph(...) / ...
    doc.save()

Any traceback is printed to stderr verbatim (the caller relays it to the
model so it can self-correct). Exit code 0 on success, 1 on a script error.
"""

import os
import shutil
import subprocess
import sys
import tempfile
import traceback
from pathlib import Path


def main(argv):
    if len(argv) not in (3, 5) or (len(argv) == 5 and argv[3] != "--type"):
        print("usage: python run.py script.py out.pdf [--type pdf|slideshow]", file=sys.stderr)
        return 2

    script_path, out_path = argv[1], argv[2]
    out_path = os.path.abspath(out_path)
    project_type = argv[4] if len(argv) == 5 else "pdf"
    if project_type not in ("pdf", "slideshow"):
        print("unsupported project type: " + project_type, file=sys.stderr)
        return 2

    # Make the `doc` module importable regardless of cwd.
    here = os.path.dirname(os.path.abspath(__file__))
    if here not in sys.path:
        sys.path.insert(0, here)

    try:
        with open(script_path, "rb") as f:
            source = f.read()
    except OSError as e:
        print(f"cannot read {script_path}: {e}", file=sys.stderr)
        return 1

    namespace = {"__name__": "__main__", "OUT_PATH": out_path, "PROJECT_TYPE": project_type}
    code = 0
    try:
        exec(compile(source, script_path, "exec"), namespace)
    except SystemExit as e:
        code = e.code if isinstance(e.code, int) else 0
    except BaseException:
        traceback.print_exc()
        return 1
    if code:
        return code

    if project_type == "slideshow":
        pptx_path = out_path + ".pptx"
        if not os.path.isfile(pptx_path):
            print("slideshow script did not create " + pptx_path, file=sys.stderr)
            return 1
        office = shutil.which("libreoffice") or shutil.which("soffice")
        if office is None:
            print("slideshow PDF conversion unavailable: libreoffice is not installed", file=sys.stderr)
            return 1
        try:
            with tempfile.TemporaryDirectory(prefix="pdf-editor-office-") as profile_dir:
                profile = Path(profile_dir).as_uri()
                cmd = [office, "-env:UserInstallation=" + profile, "--headless", "--convert-to", "pdf", "--outdir", os.path.dirname(out_path), pptx_path]
                converted = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=20, check=False)
        except subprocess.TimeoutExpired:
            print("LibreOffice slideshow conversion timed out after 20 seconds", file=sys.stderr)
            return 1
        except OSError as exc:
            print("could not start LibreOffice: " + str(exc), file=sys.stderr)
            return 1
        if converted.returncode:
            message = converted.stderr.strip() or converted.stdout.strip() or "LibreOffice exited with an error"
            print("could not convert slideshow to PDF: " + message, file=sys.stderr)
            return 1
        generated_pdf = os.path.join(os.path.dirname(out_path), os.path.basename(out_path) + ".pdf")
        if not os.path.isfile(generated_pdf):
            print("LibreOffice did not create " + generated_pdf, file=sys.stderr)
            return 1
        os.replace(generated_pdf, out_path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
