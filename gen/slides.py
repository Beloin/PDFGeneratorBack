"""Editable slideshow builder with one canonical geometry model for all outputs."""

import inspect
import json
import math
import os

from PIL import Image, ImageDraw, ImageFont
from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.enum.shapes import MSO_SHAPE
from pptx.enum.text import MSO_ANCHOR, PP_ALIGN
from pptx.util import Pt
from reportlab import rl_config


MAX_SIDE = 8192
MAX_PIXELS = 40_000_000
LEADING = 1.2
FONT_FAMILY = "Vera"


def _font_path(bold=False):
    filename = "VeraBd.ttf" if bold else "Vera.ttf"
    for directory in rl_config.TTFSearchPath:
        candidate = os.path.join(directory, filename)
        if os.path.isfile(candidate):
            return candidate
    raise RuntimeError("bundled Bitstream Vera font is missing: " + filename)


def _color(value):
    if value is None:
        return None
    if not isinstance(value, str):
        raise ValueError("colors must be six-digit hex strings")
    value = value.lstrip("#")
    if len(value) != 6:
        raise ValueError("colors must be six-digit hex strings, e.g. '#123ABC'")
    try:
        return tuple(int(value[i:i + 2], 16) for i in (0, 2, 4))
    except ValueError as exc:
        raise ValueError("invalid hex color: " + repr(value)) from exc


def _valid_number(value, label):
    if not isinstance(value, (int, float)) or not math.isfinite(value):
        raise ValueError(label + " must be a finite number")
    return float(value)


def _asset_root():
    root = os.environ.get("PDF_ASSET_ROOT", "")
    if root.startswith("file://"):
        return root[len("file://"):]
    if root.startswith("s3://"):
        raise RuntimeError("s3:// assets are not supported yet")
    return root


class Slides:
    """Create 16:9 slides by default. Coordinates are points from top-left."""

    def __init__(self, path, width=960, height=540):
        self.path = os.path.abspath(path)
        self.width = _valid_number(width, "width")
        self.height = _valid_number(height, "height")
        if self.width <= 0 or self.height <= 0:
            raise ValueError("slide dimensions must be positive")
        if self.width > MAX_SIDE or self.height > MAX_SIDE or self.width * self.height > MAX_PIXELS:
            raise ValueError("slide dimensions exceed renderer limits")
        self._slides = []
        self._slide = None
        self._ids = set()
        self._fonts = {False: {}, True: {}}

    def slide(self, background="#FFFFFF"):
        """Start a slide. The decorative background has no manifest element."""
        self._slide = {"page": len(self._slides) + 1, "background": self._hex(background), "elements": []}
        self._slides.append(self._slide)
        return self

    @staticmethod
    def _hex(value):
        color = _color(value)
        if color is None:
            return None
        return "#%02X%02X%02X" % color

    def _active(self):
        if self._slide is None:
            raise RuntimeError("call slide() before adding slide elements")
        return self._slide

    def _id(self, value):
        if not isinstance(value, str) or not value.strip():
            raise ValueError("element id must be a non-empty string")
        if value in self._ids:
            raise ValueError("duplicate element id: " + repr(value))
        self._ids.add(value)

    def _box(self, x, y, width, height):
        x = _valid_number(x, "x")
        y = _valid_number(y, "y")
        width = _valid_number(width, "width")
        height = _valid_number(height, "height")
        if width <= 0 or height <= 0 or x < 0 or y < 0 or x + width > self.width or y + height > self.height:
            raise ValueError("element bounds must fit inside the slide")
        return x, y, width, height

    def _element(self, element_id, kind, x, y, width, height, source_line, **extra):
        self._id(element_id)
        element = {"id": element_id, "type": kind, "x": x, "y": y,
                   "width": width, "height": height, "source_line": source_line}
        element.update(extra)
        self._active()["elements"].append(element)
        return element

    def _measure_font(self, bold, size):
        size_key = float(size)
        cache = self._fonts[bold]
        if size_key not in cache:
            cache[size_key] = ImageFont.truetype(_font_path(bold), max(1.0, size_key))
        return cache[size_key]

    def _wrap(self, value, font, width):
        result = []
        for paragraph in str(value).replace("\r\n", "\n").replace("\r", "\n").split("\n"):
            words = paragraph.split()
            if not words:
                result.append("")
                continue
            line = ""
            for word in words:
                # Break unusually long tokens at character boundaries so text
                # never exceeds its declared box width.
                if any(font.getlength(char) > width for char in word):
                    raise ValueError("text box width is too narrow to fit glyphs in " + repr(word))
                while font.getlength(word) > width:
                    piece = ""
                    for char in word:
                        if piece and font.getlength(piece + char) > width:
                            if line:
                                result.append(line)
                                line = ""
                            result.append(piece)
                            piece = char
                        else:
                            piece += char
                    word, piece = piece, ""
                candidate = word if not line else line + " " + word
                if line and font.getlength(candidate) > width:
                    result.append(line)
                    line = word
                else:
                    line = candidate
            result.append(line)
        return result

    def text(self, text, element_id, x, y, width, height, font_size=24, color="#000000", bold=False):
        self._active()
        x, y, width, height = self._box(x, y, width, height)
        font_size = _valid_number(font_size, "font_size")
        if font_size <= 0:
            raise ValueError("font_size must be positive")
        color = self._hex(color)
        font = self._measure_font(bool(bold), font_size)
        ascent, _ = font.getmetrics()
        leading = font_size * LEADING
        wrapped = self._wrap(text, font, width)
        lines = []
        for index, value in enumerate(wrapped):
            baseline = y + ascent + index * leading
            if baseline > y + height:
                break
            lines.append({"text": value, "x": round(x, 3), "y": round(baseline, 3)})
        line = inspect.currentframe().f_back.f_lineno
        return self._element(element_id, "text", x, y, width, height, line,
                             text=str(text), font_size=font_size, color=color,
                             bold=bool(bold), lines=lines)

    def rect(self, element_id, x, y, width, height, fill="#FFFFFF", line=None):
        self._active()
        x, y, width, height = self._box(x, y, width, height)
        return self._element(element_id, "rect", x, y, width, height,
                             inspect.currentframe().f_back.f_lineno,
                             fill=self._hex(fill), line=self._hex(line))

    def image(self, name, element_id, x, y, width=None, height=None):
        self._active()
        x = _valid_number(x, "x")
        y = _valid_number(y, "y")
        root = _asset_root()
        if not root:
            raise RuntimeError("no PDF_ASSET_ROOT set; cannot load asset " + repr(name))
        if not isinstance(name, str) or os.path.basename(name) != name or name in ("", ".", ".."):
            raise ValueError("invalid asset name " + repr(name))
        asset_path = os.path.join(root, name)
        if not os.path.isfile(asset_path):
            raise FileNotFoundError("asset not found: " + name)
        with Image.open(asset_path) as bitmap:
            aspect = bitmap.width / bitmap.height
        if width is None and height is None:
            width, height = min(120.0, self.width - x), None
        if width is None:
            width = height * aspect
        elif height is None:
            height = width / aspect
        x, y, width, height = self._box(x, y, width, height)
        return self._element(element_id, "image", x, y, width, height,
                             inspect.currentframe().f_back.f_lineno, asset_name=name)

    @staticmethod
    def _draw_rect(draw, element, scale):
        box = (round(element["x"] * scale), round(element["y"] * scale),
               round((element["x"] + element["width"]) * scale),
               round((element["y"] + element["height"]) * scale))
        if element["fill"] is not None:
            draw.rectangle(box, fill=_color(element["fill"]))
        if element["line"] is not None:
            draw.rectangle(box, outline=_color(element["line"]))

    def _cover(self):
        scale = min(1.0, 640.0 / max(self.width, self.height))
        canvas = Image.new("RGB", (max(1, round(self.width * scale)), max(1, round(self.height * scale))), "white")
        draw = ImageDraw.Draw(canvas)
        slide = self._slides[0]
        if slide["background"]:
            draw.rectangle((0, 0, canvas.width, canvas.height), fill=_color(slide["background"]))
        for element in slide["elements"]:
            if element["type"] == "rect":
                self._draw_rect(draw, element, scale)
            elif element["type"] == "text":
                font = self._measure_font(element["bold"], element["font_size"] * scale)
                color = _color(element["color"])
                layer = Image.new("RGBA", canvas.size, (0, 0, 0, 0))
                text_draw = ImageDraw.Draw(layer)
                for text_line in element["lines"]:
                    text_draw.text((text_line["x"] * scale, text_line["y"] * scale), text_line["text"],
                                   fill=color + (255,), font=font, anchor="ls")
                clip = (round(element["x"] * scale), round(element["y"] * scale),
                        round((element["x"] + element["width"]) * scale),
                        round((element["y"] + element["height"]) * scale))
                cropped = layer.crop(clip)
                canvas.paste(cropped, clip[:2], cropped)
            elif element["type"] == "image":
                root = _asset_root()
                with Image.open(os.path.join(root, element["asset_name"])) as source:
                    image = source.convert("RGBA")
                size = (max(1, round(element["width"] * scale)), max(1, round(element["height"] * scale)))
                image = image.resize(size, Image.Resampling.LANCZOS)
                canvas.paste(image, (round(element["x"] * scale), round(element["y"] * scale)), image)
        canvas.save(self.path + ".cover.png", format="PNG")

    def _pptx(self):
        deck = Presentation()
        deck.slide_width = Pt(self.width)
        deck.slide_height = Pt(self.height)
        layout = deck.slide_layouts[6]
        for slide_data in self._slides:
            slide = deck.slides.add_slide(layout)
            background = _color(slide_data["background"])
            if background is not None:
                slide.background.fill.solid()
                slide.background.fill.fore_color.rgb = RGBColor(*background)
            for element in slide_data["elements"]:
                x, y, width, height = (Pt(element[k]) for k in ("x", "y", "width", "height"))
                if element["type"] == "rect":
                    shape = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, x, y, width, height)
                    if element["fill"] is None:
                        shape.fill.background()
                    else:
                        shape.fill.solid()
                        shape.fill.fore_color.rgb = RGBColor(*_color(element["fill"]))
                    if element["line"] is None:
                        shape.line.fill.background()
                    else:
                        shape.line.color.rgb = RGBColor(*_color(element["line"]))
                elif element["type"] == "text":
                    shape = slide.shapes.add_textbox(x, y, width, height)
                    frame = shape.text_frame
                    frame.clear()
                    frame.word_wrap = False
                    frame.margin_left = frame.margin_right = frame.margin_top = frame.margin_bottom = 0
                    frame.vertical_anchor = MSO_ANCHOR.TOP
                    for index, line in enumerate(element["lines"]):
                        paragraph = frame.paragraphs[0] if index == 0 else frame.add_paragraph()
                        paragraph.alignment = PP_ALIGN.LEFT
                        paragraph.space_before = Pt(0)
                        paragraph.space_after = Pt(0)
                        paragraph.line_spacing = Pt(element["font_size"] * LEADING)
                        run = paragraph.add_run()
                        run.text = line["text"]
                        run.font.name = self._measure_font(element["bold"], element["font_size"]).getname()[0]
                        run.font.size = Pt(element["font_size"])
                        run.font.bold = element["bold"]
                        run.font.color.rgb = RGBColor(*_color(element["color"]))
                elif element["type"] == "image":
                    root = _asset_root()
                    shape = slide.shapes.add_picture(os.path.join(root, element["asset_name"]), x, y, width, height)
                else:
                    raise ValueError("unsupported slide element type " + repr(element["type"]))
                shape.name = element["id"]
        deck.save(self.path + ".pptx")

    def _manifest(self):
        elements = []
        for slide in self._slides:
            for element in slide["elements"]:
                x, y = element["x"], element["y"]
                width, height = element["width"], element["height"]
                elements.append({"id": element["id"], "type": element["type"], "page": slide["page"],
                                 "bbox": [round(x, 2), round(self.height - y - height, 2),
                                          round(x + width, 2), round(self.height - y, 2)],
                                 "line": element["source_line"]})
        with open(self.path + ".manifest.json", "w", encoding="utf-8") as stream:
            json.dump({"page_size": [self.width, self.height], "elements": elements}, stream)

    def save(self):
        if not self._slides:
            raise RuntimeError("a slideshow needs at least one slide")
        canonical = {"schema_version": 1, "width": self.width, "height": self.height,
                     "font_family": FONT_FAMILY, "fonts": {"regular": "Vera.ttf", "bold": "VeraBd.ttf"},
                     "slides": self._slides}
        with open(self.path + ".slides.json", "w", encoding="utf-8") as stream:
            json.dump(canonical, stream, ensure_ascii=False)
        self._manifest()
        self._pptx()
        self._cover()
