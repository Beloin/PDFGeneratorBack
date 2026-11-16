"""Verify slideshow conversion orchestration without requiring office IPC."""

import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import unquote, urlparse
from zipfile import ZipFile

import run


class ConversionTests(unittest.TestCase):
    def test_real_deck_conversion_paths_and_profile_cleanup(self):
        with tempfile.TemporaryDirectory(prefix="deck test ") as directory:
            root = Path(directory)
            script = root / "script.py"
            output = root / "out.pdf"
            script.write_text(
                "from slides import Slides\n"
                "deck = Slides(OUT_PATH)\n"
                "deck.slide()\n"
                "deck.text('Title', 'title', 20, 30, 300, 40)\n"
                "deck.save()\n"
            )
            profiles = []

            def convert(command, **options):
                self.assertEqual(options["timeout"], 20)
                self.assertIn("--headless", command)
                profile = next(arg for arg in command if arg.startswith("-env:UserInstallation="))
                profile_path = Path(unquote(urlparse(profile.split("=", 1)[1]).path))
                self.assertTrue(profile_path.is_dir())
                profiles.append(profile_path)
                with ZipFile(command[-1]) as deck:
                    self.assertIn("ppt/slides/slide1.xml", deck.namelist())
                # LibreOffice derives the output name by removing the .pptx suffix.
                exported = Path(command[command.index("--outdir") + 1]) / (Path(command[-1]).stem + ".pdf")
                exported.write_bytes(b"%PDF-test-converter-output")
                return subprocess.CompletedProcess(command, 0, "converted", "")

            with patch("run.shutil.which", return_value="/fake/libreoffice"), patch("run.subprocess.run", side_effect=convert):
                result = run.main(["run.py", str(script), str(output), "--type", "slideshow"])
            self.assertEqual(result, 0)
            self.assertEqual(output.read_bytes(), b"%PDF-test-converter-output")
            self.assertTrue(Path(str(output) + ".pptx").is_file())
            manifest = json.loads(Path(str(output) + ".manifest.json").read_text())
            self.assertEqual(manifest["elements"][0]["id"], "title")
            self.assertTrue(all(not profile.exists() for profile in profiles))


if __name__ == "__main__":
    unittest.main()
