package runner

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"image/png"
)

func TestRunResolvesRelativeRunnerPathBeforeChangingDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "render.sh")
	if err := os.WriteFile(path, []byte("printf '%s' '%PDF-1.4' > \"$2\"\nprintf '%s' '{\"page_size\":[1,1],\"elements\":[]}' > \"$2.manifest.json\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ppm := filepath.Join(dir, "pdftoppm")
	if err := os.WriteFile(ppm, []byte("#!/bin/sh\nprintf '\\211PNG\\r\\n\\032\\n' > \"${10}.png\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	r := New("render.sh", "sh")
	r.PDFToPPM = ppm
	result, err := r.Run("script", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("render failed: %s", result.Traceback)
	}
}

func TestRunRendersFirstPageToPNG(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	py, err := filepath.Abs("../../../gen/run.py")
	if err != nil {
		t.Fatal(err)
	}
	r := New(py, "python3")
	result, err := r.Run("from reportlab.pdfgen import canvas\nc = canvas.Canvas(OUT_PATH)\nc.drawString(30, 700, 'cover')\nc.showPage()\nc.drawString(30, 700, 'second page')\nc.save()", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("render failed: %s", result.Traceback)
	}
	if !strings.HasPrefix(string(result.CoverPNG), "\x89PNG\r\n\x1a\n") {
		t.Fatalf("cover is not PNG: %x", result.CoverPNG[:min(8, len(result.CoverPNG))])
	}
}

func TestRunTypeSlideshowConvertsPPTXToPDF(t *testing.T) {
	py := os.Getenv("PDF_PYTHON")
	if py == "" {
		py = "python3"
	}
	if err := exec.Command(py, "-c", "import pptx").Run(); err != nil {
		t.Skip("python-pptx unavailable")
	}
	resolved, err := exec.LookPath(py)
	if err != nil {
		t.Fatal(err)
	}
	py, err = filepath.Abs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	ppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Skip("pdftoppm unavailable")
	}
	converter := filepath.Join(t.TempDir(), "libreoffice")
	stub := "#!" + py + `
import sys
from pathlib import Path
from zipfile import ZipFile
from reportlab.pdfgen import canvas
with ZipFile(sys.argv[-1]) as deck:
    assert "ppt/slides/slide1.xml" in deck.namelist()
output=str(Path(sys.argv[sys.argv.index("--outdir")+1]) / (Path(sys.argv[-1]).stem+".pdf"))
c=canvas.Canvas(output,pagesize=(960,540))
c.drawString(30,480,"Converted slideshow")
c.save()
`
	if err := os.WriteFile(converter, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIBREOFFICE", converter)
	t.Setenv("PATH", "")
	r := New("../../../gen/run.py", py)
	r.PDFToPPM = ppm
	result, err := r.RunType("from slides import Slides\ns = Slides(OUT_PATH)\ns.slide('#102030')\ns.text('Editable title', 'title', 30, 30, 400, 60, font_size=32)\ns.save()", nil, "slideshow")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("slideshow render failed: %s", result.Traceback)
	}
	if len(result.PPTX) < 4 || string(result.PPTX[:2]) != "PK" {
		t.Fatalf("invalid PPTX output: %d bytes", len(result.PPTX))
	}
	if !strings.Contains(result.Manifest, `"id": "title"`) && !strings.Contains(result.Manifest, `"id":"title"`) {
		t.Fatalf("manifest missing source element: %s", result.Manifest)
	}
	var slides struct {
		SchemaVersion int     `json:"schema_version"`
		Width         float64 `json:"width"`
		Height        float64 `json:"height"`
		Slides        []struct {
			Page     int `json:"page"`
			Elements []struct {
				ID    string `json:"id"`
				Lines []struct {
					X float64 `json:"x"`
					Y float64 `json:"y"`
				} `json:"lines"`
			} `json:"elements"`
		} `json:"slides"`
	}
	if err := json.Unmarshal([]byte(result.SlidesJSON), &slides); err != nil {
		t.Fatal(err)
	}
	if slides.SchemaVersion != 1 || slides.Width != 960 || slides.Height != 540 || len(slides.Slides) != 1 || slides.Slides[0].Page != 1 || slides.Slides[0].Elements[0].ID != "title" || len(slides.Slides[0].Elements[0].Lines) != 1 {
		t.Fatalf("unexpected slide JSON: %s", result.SlidesJSON)
	}
	if !strings.HasPrefix(string(result.PDF), "%PDF-") {
		t.Fatal("slideshow did not return the converted PDF")
	}
	cover, err := png.DecodeConfig(strings.NewReader(string(result.CoverPNG)))
	if err != nil {
		t.Fatal(err)
	}
	if cover.Width != 640 || cover.Height != 360 {
		t.Fatalf("cover size %dx%d; want 640x360", cover.Width, cover.Height)
	}
}

func TestSlidesJSONValidationRejectsMalformedArtifact(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("{}"), []byte(`{"schema_version":1,"width":960,"height":540,"font_family":"Vera","fonts":{"regular":"Vera.ttf","bold":"VeraBd.ttf"},"slides":[]}`)} {
		if err := validateSlidesJSON(data); err == nil {
			t.Fatalf("accepted malformed slides JSON: %q", data)
		}
	}
}

func TestRunnerReturnsVeraFonts(t *testing.T) {
	py := os.Getenv("PDF_PYTHON")
	if py == "" {
		py = "python3"
	}
	if err := exec.Command(py, "-c", "import reportlab").Run(); err != nil {
		t.Skip("ReportLab unavailable")
	}
	r := New("unused", py)
	for _, bold := range []bool{false, true} {
		data, err := r.Font(bold)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 4 || string(data[:4]) != "\x00\x01\x00\x00" {
			t.Fatalf("unexpected Vera font signature (%t): %x", bold, data[:min(len(data), 4)])
		}
	}
}
