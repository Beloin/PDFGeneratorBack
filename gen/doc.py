"""Doc wrapper: a thin ReportLab API where every element has a stable id.

Every element records (id, type, page, bbox, source line) into a manifest
written alongside the PDF. The manifest is what lets the viewer map a clicked
region back to the script line that produced it.

Rules for AI-written scripts (enforced here):
- every element needs a unique id;
- ids are stable across edits — never rename or drop an existing id;
- an element split across pages appears as several manifest entries with the
  same id.
"""

import inspect
import json
import math
import os

from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import getSampleStyleSheet
from reportlab.platypus import (
    BaseDocTemplate,
    Flowable,
    Frame,
    Image,
    PageBreak,
    PageTemplate,
    Paragraph,
    Spacer,
    Table,
)

_PAGE_SIZE = A4


class Tagged(Flowable):
    """Wraps a flowable and records its bbox + page + line when drawn."""

    def __init__(self, inner, element_id, registry, line, tag_type):
        super().__init__()
        self.inner = inner
        self.element_id = element_id
        self.registry = registry
        self.line = line
        self._tag_type = tag_type

    def wrap(self, avail_width, avail_height):
        self.width, self.height = self.inner.wrap(avail_width, avail_height)
        return self.width, self.height

    def split(self, avail_width, avail_height):
        # A split element keeps its id on every part (multi-page elements).
        return [
            Tagged(p, self.element_id, self.registry, self.line, self._tag_type)
            for p in self.inner.split(avail_width, avail_height)
        ]

    def draw(self):
        x, y = self.canv.absolutePosition(0, 0)
        self.registry.append(
            {
                "id": self.element_id,
                "type": self._tag_type,
                "page": self.canv.getPageNumber(),
                # bbox in PDF points, origin bottom-left.
                "bbox": [round(x, 2), round(y, 2),
                         round(x + self.width, 2), round(y + self.height, 2)],
                "line": self.line,
            }
        )
        self.inner.drawOn(self.canv, 0, 0)


def _asset_path(name):
    """Resolve a doc asset name against the PDF_ASSET_ROOT URI."""
    root = os.environ.get("PDF_ASSET_ROOT", "")
    if root.startswith("file://"):
        root = root[len("file://"):]
    elif root.startswith("s3://"):
        raise RuntimeError("s3:// assets are not supported yet")
    if not root:
        raise RuntimeError(
            "no PDF_ASSET_ROOT set; cannot load asset " + repr(name)
        )
    return os.path.join(root, name)


def _svg_to_drawing(path):
    """Parse an SVG file into a reportlab Drawing (needs svglib)."""
    if not os.path.isfile(path):
        raise FileNotFoundError(
            f"SVG asset not found: {path}; add it again with add_svg before rendering"
        )
    try:
        from svglib.svglib import svg2rlg
    except ImportError as e:
        raise RuntimeError(
            "SVG embedding needs svglib: pip install svglib"
        ) from e
    drawing = svg2rlg(path)
    if drawing is None:
        raise RuntimeError("svglib could not parse " + path)
    return drawing


class _SvgFlowable(Flowable):
    """A Flowable that draws a reportlab Drawing (converted SVG) at full size."""

    def __init__(self, drawing):
        super().__init__()
        self.drawing = drawing
        self.width = drawing.width
        self.height = drawing.height

    def wrap(self, avail_width, avail_height):
        return self.width, self.height

    def draw(self):
        from reportlab.graphics import renderPDF
        renderPDF.draw(self.drawing, self.canv, 0, 0)


class Doc:
    """Build a PDF and its element manifest."""

    def __init__(self, path, pagesize=None, on_page=None):
        self.path = path
        self.pagesize = pagesize or _PAGE_SIZE
        self.on_page = on_page
        self.story = []
        self.registry = []
        self._ids = set()
        self.styles = getSampleStyleSheet()

    def _add(self, flowable, element_id, tag_type):
        if not element_id:
            raise ValueError("element id must be a non-empty string")
        if element_id in self._ids:
            raise ValueError(f"duplicate element id: {element_id!r}")
        self._ids.add(element_id)
        # stack()[2] is the caller's frame (the Doc method body is [1], _add is [0]).
        line = inspect.stack()[2].lineno
        self.story.append(Tagged(flowable, element_id, self.registry, line, tag_type))

    def heading(self, text, element_id):
        self._add(Paragraph(text, self.styles["Heading1"]), element_id, "heading")

    def paragraph(self, text, element_id):
        self._add(Paragraph(text, self.styles["BodyText"]), element_id, "paragraph")

    def spacer(self, height, element_id):
        self._add(Spacer(1, height), element_id, "spacer")

    def page_break(self, element_id):
        self._add(PageBreak(), element_id, "page_break")

    def table(self, data, element_id, col_widths=None, style=None):
        table = Table(data, colWidths=col_widths)
        table.setStyle(style or [
                ("GRID", (0, 0), (-1, -1), 0.5, (0.7, 0.7, 0.7)),
                ("FONTNAME", (0, 0), (-1, 0), "Helvetica-Bold"),
                ("BACKGROUND", (0, 0), (-1, 0), (0.9, 0.9, 0.9)),
                ("VALIGN", (0, 0), (-1, -1), "MIDDLE"),
                ("TOPPADDING", (0, 0), (-1, -1), 4),
                ("BOTTOMPADDING", (0, 0), (-1, -1), 4),
            ])
        self._add(table, element_id, "table")

    def svg(self, name, element_id):
        self._add(_SvgFlowable(_svg_to_drawing(_asset_path(name))), element_id, "svg")

    def image(self, name, element_id, width=None, height=None):
        """Embed an uploaded image; dimensions are PDF points.

        A single dimension preserves aspect ratio. With neither dimension,
        fit the image within the default page frame without enlarging it.
        """
        for value in (width, height):
            if value is not None and (not math.isfinite(value) or value <= 0):
                raise ValueError("image dimensions must be positive finite numbers")
        image = Image(_asset_path(name))
        natural_width, natural_height = image.imageWidth, image.imageHeight
        if width is None and height is None:
            scale = min(1, (self.pagesize[0] - 156) / natural_width,
                        (self.pagesize[1] - 156) / natural_height)
            width, height = natural_width * scale, natural_height * scale
        elif width is None:
            width = height * natural_width / natural_height
        elif height is None:
            height = width * natural_height / natural_width
        image.drawWidth, image.drawHeight = width, height
        self._add(image, element_id, "image")

    def save(self):
        doc = BaseDocTemplate(self.path, pagesize=self.pagesize)
        frame = Frame(doc.leftMargin, doc.bottomMargin, doc.width, doc.height, id="main")
        pt_kwargs = {"onPage": self.on_page} if self.on_page is not None else {}
        doc.addPageTemplates([PageTemplate(id="main", frames=[frame], **pt_kwargs)])
        # A blank document still needs one page for viewing and cover rendering.
        doc.build(self.story or [Spacer(1, 1)])
        with open(self.path + ".manifest.json", "w", encoding="utf-8") as f:
            json.dump(
                {"page_size": list(self.pagesize), "elements": self.registry},
                f,
                indent=2,
            )
        return self.path
