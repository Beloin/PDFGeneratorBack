package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pdfeditor/server/internal/runner"
	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/store"
	"pdfeditor/server/internal/ws"
)

func TestRealSlideshowProjectLifecycle(t *testing.T) {
	python := os.Getenv("PDF_PYTHON")
	if python == "" {
		python = "python3"
	}
	python, err := exec.LookPath(python)
	if err != nil {
		if os.Getenv("PDF_PYTHON") != "" {
			t.Fatalf("configured PDF_PYTHON is unavailable: %v", err)
		}
		t.Skip("Python unavailable")
	}
	python, err = filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(python, "-c", "import pptx, reportlab, svglib, PIL").Run(); err != nil {
		if os.Getenv("PDF_PYTHON") != "" {
			t.Fatalf("configured PDF_PYTHON is missing rendering dependencies: %v", err)
		}
		t.Skip("Python rendering dependencies unavailable")
	}
	libreOffice := os.Getenv("LIBREOFFICE")
	if libreOffice == "" {
		t.Skip("LIBREOFFICE is not configured")
	}
	libreOffice, err = exec.LookPath(libreOffice)
	if err != nil {
		t.Fatalf("configured LIBREOFFICE is unavailable: %v", err)
	}
	libreOffice, err = filepath.Abs(libreOffice)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIBREOFFICE", libreOffice)
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("PDF template cover dependency unavailable")
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
	for _, id := range []string{"slideshow-pptx", "business-pptx", "metrics-pptx"} {
		got := request(t, svc, "GET", "/templates/"+id+"/cover", "")
		if got.Code != 200 {
			t.Fatalf("template %s: %d %s", id, got.Code, got.Body.String())
		}
		if _, err := png.Decode(bytes.NewReader(got.Body.Bytes())); err != nil {
			t.Fatal(err)
		}
	}
	created := request(t, svc, "POST", "/docs", `{"type":"slideshow","template":"business-pptx","title":"Business plan"}`)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var project map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	id := project["doc_id"].(string)
	if project["type"] != "slideshow" || project["pdf_url"] == nil {
		t.Fatalf("metadata: %v", project)
	}
	scene := request(t, svc, "GET", project["slides_url"].(string), "")
	var deck struct {
		SchemaVersion int     `json:"schema_version"`
		Width         float64 `json:"width"`
		Slides        []struct {
			Elements []map[string]any `json:"elements"`
		} `json:"slides"`
		Fonts map[string]string `json:"fonts"`
	}
	if err := json.Unmarshal(scene.Body.Bytes(), &deck); err != nil {
		t.Fatal(err)
	}
	if scene.Code != 200 || deck.SchemaVersion != 1 || deck.Width != 960 || len(deck.Slides) != 4 {
		t.Fatalf("scene: %d %s", scene.Code, scene.Body.String())
	}
	for _, key := range []string{"regular_url", "bold_url"} {
		font := request(t, svc, "GET", deck.Fonts[key], "")
		if font.Code != 200 || font.Body.Len() < 1000 {
			t.Fatalf("font %s: %d", key, font.Code)
		}
	}
	pptx := request(t, svc, "GET", project["pptx_url"].(string), "")
	archive, err := zip.NewReader(bytes.NewReader(pptx.Body.Bytes()), int64(pptx.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, file := range archive.File {
		if strings.HasPrefix(file.Name, "ppt/slides/slide") && strings.HasSuffix(file.Name, ".xml") {
			count++
		}
	}
	if pptx.Code != 200 || count != 4 {
		t.Fatalf("PPTX: %d slides=%d", pptx.Code, count)
	}
	cover := request(t, svc, "GET", project["cover_url"].(string), "")
	cfg, err := png.DecodeConfig(bytes.NewReader(cover.Body.Bytes()))
	if cover.Code != 200 || err != nil || cfg.Width != 640 || cfg.Height != 360 {
		t.Fatalf("cover: %d %v %v", cover.Code, cfg, err)
	}
	pdf := request(t, svc, "GET", project["pdf_url"].(string), "")
	if pdf.Code != 200 || !bytes.HasPrefix(pdf.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("new slide PDF: %d %s", pdf.Code, pdf.Body.String())
	}

	// Insert an image, then remove its live copy and rebuild from version snapshots.
	var imageData bytes.Buffer
	bitmap := image.NewRGBA(image.Rect(0, 0, 40, 20))
	bitmap.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&imageData, bitmap); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	field, err := writer.CreateFormFile("file", "photo.png")
	if err != nil {
		t.Fatal(err)
	}
	field.Write(imageData.Bytes())
	writer.Close()
	req := httptest.NewRequest("POST", "/docs/"+id+"/images", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	uploaded := httptest.NewRecorder()
	mux.ServeHTTP(uploaded, req)
	if uploaded.Code != 201 {
		t.Fatalf("image: %d %s", uploaded.Code, uploaded.Body.String())
	}
	var asset map[string]string
	json.Unmarshal(uploaded.Body.Bytes(), &asset)
	script := "from slides import Slides\ndeck=Slides(OUT_PATH)\ndeck.slide()\ndeck.image(" + string(mustJSON(t, asset["name"])) + ", 'photo', 10, 20, width=100)\ndeck.save()\n"
	if err := st.Edit(id, 1, script); err != nil {
		t.Fatal(err)
	}
	version, err := st.Commit(id)
	if err != nil {
		t.Fatal(err)
	}
	result := svc.run(id, version.Version, false)
	var rendered struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal([]byte(resultText(result)), &rendered) != nil || !rendered.OK {
		t.Fatalf("render: %s", resultText(result))
	}
	doc, err := st.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Current().PDFKey == "" {
		t.Fatal("slideshow did not store a PDF")
	}
	for _, key := range []string{asset["key"], doc.Current().SlidesKey, doc.Current().PPTXKey, doc.Current().CoverKey, doc.Current().PDFKey} {
		if err := driver.Delete(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
	svc = New(store.NewFS(driver), runner.New(scriptPath, python), ws.NewHub(), driver)
	rebuilt := request(t, svc, "GET", "/docs/"+id+"/slides?version=2", "")
	var rebuiltScene struct {
		Slides []struct {
			Elements []struct {
				ImageURL string `json:"image_url"`
			} `json:"elements"`
		} `json:"slides"`
	}
	if rebuilt.Code != 200 || json.Unmarshal(rebuilt.Body.Bytes(), &rebuiltScene) != nil {
		t.Fatalf("rebuild: %d %s", rebuilt.Code, rebuilt.Body.String())
	}
	imageURL := rebuiltScene.Slides[0].Elements[0].ImageURL
	historical := request(t, svc, "GET", imageURL, "")
	if historical.Code != 200 || !bytes.Equal(historical.Body.Bytes(), imageData.Bytes()) {
		t.Fatalf("historical image %q: %d", imageURL, historical.Code)
	}
	rebuiltPDF := request(t, svc, "GET", "/docs/"+id+"/pdf?version=2", "")
	if rebuiltPDF.Code != 200 || !bytes.HasPrefix(rebuiltPDF.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("rebuilt PDF: %d %s", rebuiltPDF.Code, rebuiltPDF.Body.String())
	}
	doc, err = svc.store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Current().Version != 2 {
		t.Fatalf("rebuild committed version %d", doc.Current().Version)
	}
	// Backfill legacy metadata by regenerating outputs at the existing version.
	vp, _ := store.VersionPrefix(id, 2)
	legacyKey := vp + "/pdf_render.pdf"
	driver.Put(context.Background(), legacyKey, []byte("%PDF-legacy"))
	doc.Versions[1].SlidesKey = ""
	doc.Versions[1].PDFKey = legacyKey
	prefix, _ := store.ProjectPrefix(id)
	driver.Put(context.Background(), prefix+"/project.json", mustJSON(t, doc))
	legacy := request(t, svc, "GET", "/docs/"+id+"/slides?version=2", "")
	if legacy.Code != 200 {
		t.Fatalf("legacy backfill: %d %s", legacy.Code, legacy.Body.String())
	}
	saved := request(t, svc, "GET", "/docs/"+id+"/pdf?version=2", "")
	if saved.Code != 200 || !bytes.HasPrefix(saved.Body.Bytes(), []byte("%PDF-")) || saved.Body.String() == "%PDF-legacy" {
		t.Fatalf("legacy PDF: status %d, size %d", saved.Code, saved.Body.Len())
	}
	doc, err = svc.store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Current().Version != 2 || doc.Current().PDFKey != legacyKey {
		t.Fatalf("legacy backfill changed version or PDF key: %+v", doc.Current())
	}
	if err := svc.store.Edit(id, 2, "raise RuntimeError('failed edit')"); err != nil {
		t.Fatal(err)
	}
	failed, err := svc.store.Commit(id)
	if err != nil {
		t.Fatal(err)
	}
	svc.run(id, failed.Version, false)
	metadata := request(t, svc, "GET", "/docs/"+id, "")
	var current map[string]any
	json.Unmarshal(metadata.Body.Bytes(), &current)
	if current["rendered_version"] != float64(2) || current["version"] != float64(3) {
		t.Fatalf("failed version metadata: %v", current)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
