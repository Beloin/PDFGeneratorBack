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
import sys
import traceback


def main(argv):
    if len(argv) != 3:
        print("usage: python run.py script.py out.pdf", file=sys.stderr)
        return 2

    script_path, out_path = argv[1], argv[2]

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

    namespace = {"__name__": "__main__", "OUT_PATH": out_path}
    try:
        exec(compile(source, script_path, "exec"), namespace)
    except SystemExit as e:
        return e.code if isinstance(e.code, int) else 0
    except BaseException:
        traceback.print_exc()
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
