package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
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

// Exercise real Python rendering and Poppler conversion through the HTTP API.
func TestRealRenderingWithUploadedImageAndSource(t *testing.T) {
	python := os.Getenv("PDF_PYTHON")
	if python == "" {
		python = "python3"
	}
	for _, tool := range []string{python, "pdftoppm", "pdftotext"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("integration dependency %s unavailable", tool)
		}
	}
	if err := exec.Command(python, "-c", "import reportlab, svglib").Run(); err != nil {
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
	for id := range Templates {
		if templateType(id) == "slideshow" {
			t.Run(id+"-cover", func(t *testing.T) {
				if !slideshowConversionAvailable(t) {
					t.Skip("sandbox denies Unix socket binding required by LibreOffice")
				}
				if err := exec.Command(python, "-c", "import pptx").Run(); err != nil {
					t.Skip("python-pptx unavailable")
				}
				response := request(t, svc, "GET", "/templates/"+id+"/cover", "")
				if response.Code != 200 {
					t.Fatalf("template cover: %d %s", response.Code, response.Body.String())
				}
				if _, err := png.Decode(bytes.NewReader(response.Body.Bytes())); err != nil {
					t.Fatal(err)
				}
			})
			continue
		}
		response := request(t, svc, "GET", "/templates/"+id+"/cover", "")
		if response.Code != 200 {
			t.Fatalf("template %s cover: %d %s", id, response.Code, response.Body.String())
		}
		if _, err := png.Decode(bytes.NewReader(response.Body.Bytes())); err != nil {
			t.Fatal(err)
		}
	}
	created := request(t, svc, "POST", "/docs", `{"template":"report"}`)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var project struct {
		ID       string `json:"doc_id"`
		CoverURL string `json:"cover_url"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.CoverURL == "" {
		t.Fatal("missing cover URL")
	}
	cover := request(t, svc, "GET", project.CoverURL, "")
	if cover.Code != 200 {
		t.Fatalf("cover: %d", cover.Code)
	}
	if _, err := png.Decode(bytes.NewReader(cover.Body.Bytes())); err != nil {
		t.Fatal(err)
	}

	upload := func(endpoint, filename string, data []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		field, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := field.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", endpoint, &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		mux := http.NewServeMux()
		svc.RegisterRoutes(mux)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != 201 {
			t.Fatalf("upload %s: %d %s", filename, response.Code, response.Body.String())
		}
		return response
	}
	pdf := request(t, svc, "GET", "/docs/"+project.ID+"/pdf", "")
	source := upload("/docs/"+project.ID+"/sources", "report.pdf", pdf.Body.Bytes())
	var reference struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(source.Body.Bytes(), &reference); err != nil {
		t.Fatal(err)
	}
	text := request(t, svc, "GET", "/docs/"+project.ID+"/sources/"+reference.ID, "")
	if text.Code != 200 || !strings.Contains(text.Body.String(), "Quarterly Sales Report") {
		t.Fatalf("converted source: %d %s", text.Code, text.Body.String())
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 40, 20))); err != nil {
		t.Fatal(err)
	}
	uploaded := upload("/docs/"+project.ID+"/images", "photo.png", imageBytes.Bytes())
	var asset struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(uploaded.Body.Bytes(), &asset); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("from doc import Doc\ndoc = Doc(OUT_PATH)\ndoc.image(%q, 'photo', width=100)\ndoc.save()\n", asset.Name)
	if err := st.Edit(project.ID, 1, script); err != nil {
		t.Fatal(err)
	}
	version, err := st.Commit(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := svc.run(project.ID, version.Version, false)
	var rendered struct {
		OK bool `json:"ok"`
	}
	if result.IsError || json.Unmarshal([]byte(resultText(result)), &rendered) != nil || !rendered.OK {
		t.Fatalf("render image: %s", resultText(result))
	}
	doc, err := st.Get(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Current().Manifest, `"image"`) {
		t.Fatal("image absent from manifest")
	}
	// Remove live image and outputs: historical regeneration must use the snapshot.
	for _, key := range []string{asset.Key, doc.Current().PDFKey, doc.Current().CoverKey} {
		if err := driver.Delete(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
	rebuilt := request(t, svc, "GET", fmt.Sprintf("/docs/%s/cover?version=%d", project.ID, version.Version), "")
	if rebuilt.Code != 200 {
		t.Fatalf("historical cover: %d %s", rebuilt.Code, rebuilt.Body.String())
	}
	if _, err := png.Decode(bytes.NewReader(rebuilt.Body.Bytes())); err != nil {
		t.Fatal(err)
	}
	deleted := request(t, svc, "DELETE", "/docs/"+project.ID, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	keys, err := driver.List(context.Background(), "pdf/"+project.ID)
	if err != nil || len(keys) != 0 {
		t.Fatalf("project files remain after delete: %v, %v", keys, err)
	}
}
