import json
import os
import tempfile
import unittest

from PIL import Image

from slides import Slides


class SlidesTests(unittest.TestCase):
    def test_elements_create_pptx_and_pdf_point_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            output = os.path.join(directory, "deck.pdf")
            slides = Slides(output)
            slides.slide("#102030")
            slides.rect("panel", 20, 30, 300, 200, fill="#FFFFFF", line="#000000")
            slides.text("Title", "title", 30, 40, 250, 40, font_size=28, bold=True)
            slides.slide()
            Image.new("RGB", (120, 60), "red").save(os.path.join(directory, "photo.png"))
            old_root = os.environ.get("PDF_ASSET_ROOT")
            os.environ["PDF_ASSET_ROOT"] = "file://" + directory
            try:
                slides.image("photo.png", "photo", 100, 100, width=120)
            finally:
                if old_root is None:
                    os.environ.pop("PDF_ASSET_ROOT", None)
                else:
                    os.environ["PDF_ASSET_ROOT"] = old_root
            slides.save()
            self.assertTrue(os.path.isfile(output + ".pptx"))
            with open(output + ".manifest.json", encoding="utf-8") as stream:
                manifest = json.load(stream)
            self.assertEqual(manifest["page_size"], [960.0, 540.0])
            self.assertEqual([item["id"] for item in manifest["elements"]], ["panel", "title", "photo"])
            self.assertEqual([item["page"] for item in manifest["elements"]], [1, 1, 2])
            self.assertEqual(manifest["elements"][0]["bbox"], [20, 310, 320, 510])
            self.assertGreater(manifest["elements"][1]["line"], 0)
            self.assertEqual(manifest["elements"][2]["bbox"], [100, 380, 220, 440])

    def test_duplicate_ids_and_non_finite_bounds_are_rejected(self):
        slides = Slides("unused.pdf")
        slides.slide()
        slides.rect("shape", 0, 0, 10, 10)
        with self.assertRaises(ValueError):
            slides.rect("shape", 20, 20, 10, 10)
        with self.assertRaises(ValueError):
            slides.rect("bad", float("inf"), 0, 10, 10)


if __name__ == "__main__":
    unittest.main()
