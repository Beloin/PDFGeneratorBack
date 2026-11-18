"""Verify LibreOffice command construction and conversion failure handling."""

from contextlib import redirect_stderr
import io
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from zipfile import ZipFile

import run


class RunnerWrapperTests(unittest.TestCase):
    def test_slideshow_converts_generated_pptx_and_cleans_profile(self):
        with tempfile.TemporaryDirectory(prefix='deck "quoted" ') as directory:
            script = Path(directory) / "script.py"
            output = Path(directory) / "deck.pdf"
            script.write_text("from slides import Slides\ndeck=Slides(OUT_PATH)\ndeck.slide('#112233')\ndeck.text('Title', 'title', 20, 30, 300, 40)\ndeck.save()\n")
            seen = []
            def convert(command, **options):
                self.assertEqual(command[0], "/fake/libreoffice")
                self.assertIn("--headless", command)
                self.assertEqual(command[command.index("--convert-to") + 1], "pdf:impress_pdf_Export")
                self.assertEqual(options["timeout"], 20)
                directory = Path(command[command.index("--outdir") + 1])
                seen.append(directory)
                self.assertEqual(command[1], "-env:UserInstallation=" + (directory / "profile").as_uri())
                with ZipFile(command[-1]) as deck:
                    self.assertIn("ppt/slides/slide1.xml", deck.namelist())
                self.assertEqual(command[-1], str(output) + ".pptx")
                converted = directory / (Path(command[-1]).stem + ".pdf")
                converted.write_bytes(b"%PDF-converted")
                return subprocess.CompletedProcess(command, 0, "", "")
            with patch("run.shutil.which", return_value="/fake/libreoffice"), patch("run.subprocess.run", side_effect=convert):
                self.assertEqual(run.main(["run.py", str(script), str(output), "--type", "slideshow"]), 0)
            self.assertEqual(output.read_bytes(), b"%PDF-converted")
            for suffix in (".slides.json", ".pptx", ".manifest.json", ".cover.png"):
                self.assertTrue(Path(str(output) + suffix).is_file(), suffix)
            self.assertTrue(all(not path.exists() for path in seen))

    def test_missing_invalid_failed_and_timed_out_conversion_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "deck.pdf"
            cases = [subprocess.CompletedProcess([], 0, "", ""), subprocess.CompletedProcess([], 2, "", "conversion error"), subprocess.TimeoutExpired([], 20)]
            for outcome in cases:
                with self.subTest(outcome=outcome):
                    output.write_bytes(b"%PDF-stale-preview")
                    kwargs = {"side_effect": outcome} if isinstance(outcome, Exception) else {"return_value": outcome}
                    with patch("run.shutil.which", return_value="/fake/libreoffice"), patch("run.subprocess.run", **kwargs), redirect_stderr(io.StringIO()):
                        self.assertEqual(run.convert_slideshow(str(output)), 1)
                    self.assertFalse(output.exists())
            with patch("run.shutil.which", return_value=None), redirect_stderr(io.StringIO()):
                self.assertEqual(run.convert_slideshow(str(output)), 1)

    def test_slideshow_fails_if_script_omits_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            script = Path(directory) / "script.py"
            output = Path(directory) / "deck.pdf"
            script.write_text("pass\n", encoding="utf-8")
            with redirect_stderr(io.StringIO()):
                self.assertEqual(run.main(["run.py", str(script), str(output), "--type", "slideshow"]), 1)


if __name__ == "__main__":
    unittest.main()
