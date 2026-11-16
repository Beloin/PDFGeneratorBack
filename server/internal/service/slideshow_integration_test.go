package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"pdfeditor/server/internal/runner"
	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/store"
	"pdfeditor/server/internal/ws"
)

func slideshowConversionAvailable(t *testing.T) bool {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "office.sock"))
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	return true
}

func TestRealSlideshowProjectLifecycle(t *testing.T) {
	if !slideshowConversionAvailable(t) {
		t.Skip("sandbox denies Unix socket binding required by LibreOffice")
	}
	python := os.Getenv("PDF_PYTHON")
	if python == "" {
		python = "python3"
	}
	for _, tool := range []string{python, "libreoffice", "pdftoppm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("missing integration dependency %s", tool)
		}
	}
	if err := exec.Command(python, "-c", "import pptx, reportlab, svglib").Run(); err != nil {
		t.Skip("Python rendering dependencies unavailable")
	}
	t.Setenv("AUTH_TOKEN", "")
	driver, err := storage.NewDriverFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewFS(driver)
	scriptPath, err := filepath.Abs("../../../gen/run.py")
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, runner.New(scriptPath, python), ws.NewHub(), driver)
	if err := svc.SeedTemplates(); err != nil {
		t.Fatal(err)
	}
	response := request(t, svc, "POST", "/docs", `{"type":"slideshow","template":"business-pptx","title":"Business plan"}`)
	if response.Code != 201 {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var project struct {
		ID       string `json:"doc_id"`
		Type     string `json:"type"`
		PPTXURL  string `json:"pptx_url"`
		PDFURL   string `json:"pdf_url"`
		CoverURL string `json:"cover_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.Type != "slideshow" || project.PPTXURL == "" {
		t.Fatalf("project: %+v", project)
	}
	deck := request(t, svc, "GET", project.PPTXURL, "")
	if deck.Code != 200 {
		t.Fatalf("pptx: %d %s", deck.Code, deck.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(deck.Body.Bytes()), int64(deck.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	slides := 0
	for _, file := range archive.File {
		if strings.HasPrefix(file.Name, "ppt/slides/slide") && strings.HasSuffix(file.Name, ".xml") {
			slides++
		}
	}
	if slides != 4 {
		t.Fatalf("slides=%d", slides)
	}
	pdf := request(t, svc, "GET", project.PDFURL, "")
	if pdf.Code != 200 || !bytes.HasPrefix(pdf.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("PDF: %d", pdf.Code)
	}
	cover := request(t, svc, "GET", project.CoverURL, "")
	if _, err := png.Decode(bytes.NewReader(cover.Body.Bytes())); cover.Code != 200 || err != nil {
		t.Fatalf("cover: %d %v", cover.Code, err)
	}
	doc, err := st.Get(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		PageSize []float64 `json:"page_size"`
		Elements []element `json:"elements"`
	}
	if err := json.Unmarshal([]byte(doc.Current().Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Elements) == 0 || len(manifest.PageSize) != 2 || manifest.PageSize[0] != 960 {
		t.Fatalf("manifest: %+v", manifest)
	}
	// Reopen through a fresh store and rebuild the first version's missing deck.
	if err := driver.Delete(context.Background(), doc.Current().PPTXKey); err != nil {
		t.Fatal(err)
	}
	reopened := New(store.NewFS(driver), runner.New(scriptPath, python), ws.NewHub(), driver)
	rebuilt := request(t, reopened, "GET", project.PPTXURL, "")
	if rebuilt.Code != 200 {
		t.Fatalf("rebuild: %d %s", rebuilt.Code, rebuilt.Body.String())
	}
	if _, err := zip.NewReader(bytes.NewReader(rebuilt.Body.Bytes()), int64(rebuilt.Body.Len())); err != nil {
		t.Fatal(err)
	}
	versions := request(t, reopened, "GET", "/docs/"+project.ID+"/versions", "")
	if versions.Code != 200 || !strings.Contains(versions.Body.String(), `"pptx_key"`) {
		t.Fatalf("versions: %d %s", versions.Code, versions.Body.String())
	}
}
