package runner

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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

func TestRunTypeSlideshowProducesEditableDeckAndPDF(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "office.sock"))
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skip("sandbox denies Unix socket binding required by LibreOffice")
	}
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if _, err := exec.LookPath("libreoffice"); err != nil {
		t.Skip("libreoffice is required for slideshow rendering")
	}
	py := os.Getenv("PDF_PYTHON")
	if py == "" {
		py = "python3"
	}
	if err := exec.Command(py, "-c", "import pptx").Run(); err != nil {
		t.Skip("python-pptx unavailable")
	}
	r := New("../../../gen/run.py", py)
	result, err := r.RunType("from slides import Slides\ns = Slides(OUT_PATH)\ns.slide('#102030')\ns.text('Editable title', 'title', 30, 30, 400, 60, font_size=32)\ns.save()", nil, "slideshow")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("slideshow render failed: %s", result.Traceback)
	}
	if len(result.PDF) < 5 || string(result.PDF[:5]) != "%PDF-" {
		t.Fatalf("invalid PDF output: %d bytes", len(result.PDF))
	}
	if len(result.PPTX) < 4 || string(result.PPTX[:2]) != "PK" {
		t.Fatalf("invalid PPTX output: %d bytes", len(result.PPTX))
	}
	if !strings.Contains(result.Manifest, `"id": "title"`) && !strings.Contains(result.Manifest, `"id":"title"`) {
		t.Fatalf("manifest missing source element: %s", result.Manifest)
	}
}
