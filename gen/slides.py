"""Minimal, scriptable PowerPoint slideshow builder with a PDF-point manifest.

Coordinates use points from the top-left of each slide. ``save`` writes an
editable ``.pptx`` beside the OUT_PATH passed to ``Slides`` and the same
element manifest format used by ``doc.Doc``.
"""

import inspect
import json
import math
import os

from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.enum.shapes import MSO_SHAPE
from pptx.enum.text import MSO_ANCHOR, PP_ALIGN
from pptx.util import Pt


def _color(value):
    if value is None:
        return None
    value = value.lstrip("#")
    if len(value) != 6:
        raise ValueError("colors must be six-digit hex strings, e.g. '#123ABC'")
    try:
        return RGBColor.from_string(value.upper())
    except ValueError as exc:
        raise ValueError("invalid hex color: " + repr(value)) from exc


class Slides:
    """Create a 16:9 slideshow by default, with element-level source mapping."""

    def __init__(self, path, width=960, height=540):
        if width <= 0 or height <= 0:
            raise ValueError("slide dimensions must be positive")
        self.path = path
        self.width = float(width)
        self.height = float(height)
        self.presentation = Presentation()
        self.presentation.slide_width = Pt(self.width)
        self.presentation.slide_height = Pt(self.height)
        self._layout = self.presentation.slide_layouts[6]
        self._slide = None
        self._elements = []
        self._ids = set()

    def slide(self, background="#FFFFFF"):
        """Start a slide and set its background color (background is decorative)."""
        slide = self.presentation.slides.add_slide(self._layout)
        color = _color(background)
        if color is not None:
            slide.background.fill.solid()
            slide.background.fill.fore_color.rgb = color
        self._slide = slide
        return self

    def _active(self):
        if self._slide is None:
            raise RuntimeError("call slide() before adding slide elements")
        return self._slide

    def _box(self, x, y, width, height):
        values = (x, y, width, height)
        if any(not isinstance(v, (int, float)) or not math.isfinite(v) for v in values):
            raise ValueError("element coordinates and dimensions must be numbers")
        if width <= 0 or height <= 0 or x < 0 or y < 0 or x + width > self.width or y + height > self.height:
            raise ValueError("element bounds must fit inside the slide")
        return Pt(x), Pt(y), Pt(width), Pt(height)

    def _validate_id(self, element_id):
        if not isinstance(element_id, str) or not element_id.strip():
            raise ValueError("element id must be a non-empty string")
        if element_id in self._ids:
            raise ValueError("duplicate element id: " + repr(element_id))

    def _record(self, element_id, kind, x, y, width, height, line):
        self._validate_id(element_id)
        self._ids.add(element_id)
        bottom = self.height - y - height
        self._elements.append({
            "id": element_id,
            "type": kind,
            "page": len(self.presentation.slides),
            "bbox": [round(x, 2), round(bottom, 2), round(x + width, 2), round(bottom + height, 2)],
            "line": line,
        })

    def text(self, text, element_id, x, y, width, height, font_size=24, color="#000000", bold=False):
        slide = self._active()
        self._validate_id(element_id)
        if not isinstance(font_size, (int, float)) or not math.isfinite(font_size) or font_size <= 0:
            raise ValueError("font_size must be a positive finite number")
        left, top, w, h = self._box(x, y, width, height)
        shape = slide.shapes.add_textbox(left, top, w, h)
        shape.name = element_id
        frame = shape.text_frame
        frame.clear()
        frame.margin_left = 0
        frame.margin_right = 0
        frame.margin_top = 0
        frame.margin_bottom = 0
        frame.word_wrap = True
        frame.vertical_anchor = MSO_ANCHOR.TOP
        paragraph = frame.paragraphs[0]
        paragraph.alignment = PP_ALIGN.LEFT
        run = paragraph.add_run()
        run.text = str(text)
        run.font.size = Pt(font_size)
        run.font.bold = bool(bold)
        run.font.color.rgb = _color(color)
        self._record(element_id, "text", x, y, width, height, inspect.currentframe().f_back.f_lineno)
        return shape

    def rect(self, element_id, x, y, width, height, fill="#FFFFFF", line=None):
        slide = self._active()
        self._validate_id(element_id)
        left, top, w, h = self._box(x, y, width, height)
        shape = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, left, top, w, h)
        shape.name = element_id
        if fill is None:
            shape.fill.background()
        else:
            shape.fill.solid()
            shape.fill.fore_color.rgb = _color(fill)
        if line is None:
            shape.line.fill.background()
        else:
            shape.line.color.rgb = _color(line)
        self._record(element_id, "rect", x, y, width, height, inspect.currentframe().f_back.f_lineno)
        return shape

    def image(self, name, element_id, x, y, width=None, height=None):
        slide = self._active()
        self._validate_id(element_id)
        root = os.environ.get("PDF_ASSET_ROOT", "")
        if root.startswith("file://"):
            root = root[len("file://"):]
        elif root.startswith("s3://"):
            raise RuntimeError("s3:// assets are not supported yet")
        if not root:
            raise RuntimeError("no PDF_ASSET_ROOT set; cannot load asset " + repr(name))
        if not isinstance(name, str) or os.path.basename(name) != name or name in ("", ".", ".."):
            raise ValueError("invalid asset name " + repr(name))
        asset_path = os.path.join(root, name)
        if not os.path.isfile(asset_path):
            raise FileNotFoundError("asset not found: " + name)
        from PIL import Image as PILImage
        with PILImage.open(asset_path) as bitmap:
            aspect = bitmap.width / bitmap.height
        if width is None and height is None:
            width = 120.0
            height = width / aspect
        elif width is None:
            width = height * aspect
        elif height is None:
            height = width / aspect
        left, top, w, h = self._box(x, y, width, height)
        shape = slide.shapes.add_picture(asset_path, left, top, width=w, height=h)
        shape.name = element_id
        self._record(element_id, "image", x, y, width, height, inspect.currentframe().f_back.f_lineno)
        return shape

    def save(self):
        if not self.presentation.slides:
            raise RuntimeError("a slideshow needs at least one slide")
        self.presentation.save(self.path + ".pptx")
        with open(self.path + ".manifest.json", "w", encoding="utf-8") as stream:
            json.dump({"page_size": [self.width, self.height], "elements": self._elements}, stream)

