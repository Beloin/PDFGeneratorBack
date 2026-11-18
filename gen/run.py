#!/usr/bin/env python3
"""Run an AI-written PDF Doc or editable slideshow script.

Usage: python run.py script.py out.pdf

For PDF scripts, the namespace exposes OUT_PATH and expects:

    from doc import Doc
    doc = Doc(OUT_PATH)
    ... doc.heading(...) / doc.paragraph(...) / ...
    doc.save()

For slideshow scripts, Slides.save() writes out.pdf.slides.json, out.pdf.pptx,
out.pdf.manifest.json, and out.pdf.cover.png. LibreOffice headless
converts the generated PPTX to out.pdf for the browser preview.

Any traceback is printed to stderr verbatim (the caller relays it to the
model so it can self-correct). Exit code 0 on success, 1 on a script error.
"""

import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import traceback


def convert_slideshow(out_path):
    """Convert the editable deck using the Heph-provided LibreOffice tool."""
    configured = os.environ.get("LIBREOFFICE", "")
    binary = shutil.which(configured or "libreoffice")
    if not binary and not configured:
        binary = shutil.which("soffice")
    if not binary:
        print("LibreOffice not found; install LibreOffice Impress or set LIBREOFFICE", file=sys.stderr)
        return 1
    with tempfile.TemporaryDirectory(prefix="pdf-editor-libreoffice-") as directory:
        # Separate profiles prevent concurrent jobs from attaching to another process.
        profile = Path(directory, "profile").as_uri()
        converted_path = Path(directory, Path(out_path + ".pptx").stem + ".pdf")
        # A stale preview must not make an unsuccessful conversion look valid.
        if os.path.exists(out_path):
            os.remove(out_path)
        try:
            converted = subprocess.run(
                [binary, "-env:UserInstallation=" + profile, "--headless", "--nologo",
                 "--nodefault", "--norestore", "--convert-to", "pdf:impress_pdf_Export",
                 "--outdir", directory, out_path + ".pptx"],
                capture_output=True, text=True, timeout=20, check=False,
            )
        except subprocess.TimeoutExpired:
            print("LibreOffice slideshow conversion timed out after 20 seconds", file=sys.stderr)
            return 1
        except OSError as exc:
            print("could not start LibreOffice: " + str(exc), file=sys.stderr)
            return 1
        if converted.returncode:
            detail = converted.stderr.strip() or converted.stdout.strip() or f"exit code {converted.returncode}"
            print("LibreOffice slideshow conversion failed: " + detail, file=sys.stderr)
            return 1
        try:
            with open(converted_path, "rb") as output:
                valid = output.read(5) == b"%PDF-"
        except OSError:
            valid = False
        if not valid:
            detail = converted.stderr.strip() or converted.stdout.strip()
            print("LibreOffice did not produce a valid PDF" + (": " + detail if detail else ""), file=sys.stderr)
            return 1
        os.replace(converted_path, out_path)
    return 0


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
        expected = (".slides.json", ".pptx", ".manifest.json", ".cover.png")
        missing = [out_path + suffix for suffix in expected if not os.path.isfile(out_path + suffix)]
        if missing:
            print("slideshow script did not create required artifact(s): " + ", ".join(missing), file=sys.stderr)
            return 1
        return convert_slideshow(out_path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
