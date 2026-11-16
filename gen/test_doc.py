"""Image embedding exercises the PDF and manifest output together."""

import json
import os
import tempfile
import unittest
from unittest.mock import patch

from PIL import Image

from doc import Doc


class ImageTests(unittest.TestCase):
    def test_blank_document_has_a_page_and_empty_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            output = os.path.join(directory, "blank.pdf")
            Doc(output).save()
            with open(output, "rb") as pdf:
                self.assertIn(b"/Count 1", pdf.read())
            with open(output + ".manifest.json", encoding="utf-8") as manifest:
                self.assertEqual(json.load(manifest)["elements"], [])

    def test_uploaded_image_preserves_ratio_and_manifest_id(self):
        with tempfile.TemporaryDirectory() as directory:
            Image.new("RGB", (200, 100), "blue").save(os.path.join(directory, "photo.png"))
            output = os.path.join(directory, "document.pdf")
            with patch.dict(os.environ, {"PDF_ASSET_ROOT": "file://" + directory}):
                document = Doc(output)
                document.image("photo.png", "uploaded-photo", width=100)
                document.save()
            with open(output + ".manifest.json", encoding="utf-8") as manifest:
                element = json.load(manifest)["elements"][0]
            self.assertEqual(element["id"], "uploaded-photo")
            self.assertEqual(element["type"], "image")
            self.assertEqual(element["page"], 1)
            x0, y0, x1, y1 = element["bbox"]
            self.assertAlmostEqual(x1 - x0, 100)
            self.assertAlmostEqual(y1 - y0, 50)
            with open(output, "rb") as pdf:
                self.assertEqual(pdf.read(5), b"%PDF-")

    def test_invalid_dimensions_are_rejected(self):
        for dimension in (0, -1, float("inf"), float("nan")):
            with self.subTest(dimension=dimension), self.assertRaises(ValueError):
                Doc("unused.pdf").image("unused.png", "image", width=dimension)


if __name__ == "__main__":
    unittest.main()
