package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pdfeditor/server/internal/runner"
	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/store"
	"pdfeditor/server/internal/ws"
)

func testService(t *testing.T, shell string) (*Service, *storage.DriverFS, *store.FS) {
	t.Helper()
	dir := t.TempDir()
	sh := filepath.Join(dir, "render.sh")
	if err := os.WriteFile(sh, []byte(shell), 0o644); err != nil {
		t.Fatal(err)
	}
	ppm := filepath.Join(dir, "pdftoppm")
	if err := os.WriteFile(ppm, []byte("#!/bin/sh\nprintf '\\211PNG\\r\\n\\032\\n' > \"${10}.png\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	driver, err := storage.NewDriverFS(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewFS(driver)
	r := runner.New(sh, "sh")
	r.PDFToPPM = ppm
	return New(st, r, ws.NewHub(), driver), driver, st
}

const validRender = "printf '%s' '%PDF-1.4' > \"$2\"\nprintf '%s' '{\"page_size\":[1,1],\"elements\":[]}' > \"$2.manifest.json\"\nif [ \"$4\" = slideshow ]; then\n" +
	"python3 -c 'import sys,zipfile; z=zipfile.ZipFile(sys.argv[1], \"w\"); z.writestr(\"ppt/presentation.xml\", \"<p:presentation/>\"); z.close()' \"$2.pptx\"\n" +
	"cat > \"$2.slides.json\" <<'JSON'\n{\"schema_version\":1,\"width\":960,\"height\":540,\"font_family\":\"Vera\",\"fonts\":{\"regular\":\"Vera.ttf\",\"bold\":\"VeraBd.ttf\"},\"slides\":[{\"page\":1,\"background\":\"#FFFFFF\",\"elements\":[{\"id\":\"image\",\"type\":\"image\",\"x\":0,\"y\":0,\"width\":10,\"height\":10,\"source_line\":1,\"asset_name\":\"photo.png\"}]}]}\nJSON\n" +
	"printf '%s' 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGNgYGAAAAAEAAH2FzhVAAAAAElFTkSuQmCC' | base64 -d > \"$2.cover.png\"\nfi\n"

func request(t *testing.T, svc *Service, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestProjectLifecycle(t *testing.T) {
	svc, driver, st := testService(t, validRender)
	if err := svc.SeedTemplates(); err != nil {
		t.Fatal(err)
	}
	if got := request(t, svc, "GET", "/docs", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"docs":[]`) {
		t.Fatalf("initial projects: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, svc, "GET", "/templates", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"id":"report"`) || !strings.Contains(got.Body.String(), `"id":"business"`) || !strings.Contains(got.Body.String(), `"id":"metrics"`) {
		t.Fatalf("templates: %d %s", got.Code, got.Body.String())
	}
	created := request(t, svc, "POST", "/docs", `{"template":"report","title":"My report"}`)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var project struct {
		ID      string `json:"doc_id"`
		Title   string `json:"title"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID == "" || project.Title != "My report" || project.Version != 1 {
		t.Fatalf("project: %+v", project)
	}
	if got := request(t, svc, "GET", "/docs", ""); got.Code != 200 || !strings.Contains(got.Body.String(), project.ID) {
		t.Fatalf("list: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, svc, "GET", "/docs/"+project.ID, ""); got.Code != 200 {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, svc, "PATCH", "/docs/"+project.ID, `{"title":"Renamed"}`); got.Code != 200 || !strings.Contains(got.Body.String(), "Renamed") {
		t.Fatalf("rename: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, svc, "GET", "/docs/"+project.ID+"/pdf", ""); got.Code != 200 || got.Body.String() != "%PDF-1.4" {
		t.Fatalf("pdf: %d %s", got.Code, got.Body.String())
	}
	cover := request(t, svc, "GET", "/docs/"+project.ID+"/cover?version=1", "")
	if cover.Code != 200 || !strings.HasPrefix(cover.Body.String(), "\x89PNG") {
		t.Fatalf("cover: %d %x", cover.Code, cover.Body.Bytes())
	}
	if err := st.Edit(project.ID, 1, "failed draft"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Commit(project.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(t, svc, "GET", "/docs/"+project.ID, ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"rendered_version":1`) || !strings.Contains(got.Body.String(), `"version":2`) {
		t.Fatalf("last rendered metadata: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, svc, "GET", "/docs/"+project.ID+"/manifest?version=1", ""); got.Code != 200 {
		t.Fatalf("versioned manifest: %d %s", got.Code, got.Body.String())
	}
	keys, err := driver.List(context.Background(), "pdf/"+project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) < 4 {
		t.Fatalf("project keys: %v", keys)
	}
	if got := request(t, svc, "DELETE", "/docs/"+project.ID, ""); got.Code != 204 {
		t.Fatalf("delete: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, svc, "GET", "/docs", ""); got.Code != 200 || strings.Contains(got.Body.String(), project.ID) {
		t.Fatalf("list after delete: %d %s", got.Code, got.Body.String())
	}
}

func TestCreateRejectsInvalidTemplateAndTitle(t *testing.T) {
	svc, _, _ := testService(t, validRender)
	if err := svc.SeedTemplates(); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"template":"../escape"}`, `{"template":"unknown"}`, `{"template":"report","title":"  "}`} {
		got := request(t, svc, "POST", "/docs", body)
		if got.Code != 400 {
			t.Fatalf("body %s: %d %s", body, got.Code, got.Body.String())
		}
	}
}

func TestProjectTypeSelectionAndTemplateFilter(t *testing.T) {
	svc, driver, st := testService(t, validRender)
	if err := svc.SeedTemplates(); err != nil {
		t.Fatal(err)
	}
	filtered := request(t, svc, "GET", "/templates?type=slideshow", "")
	if filtered.Code != http.StatusOK || !strings.Contains(filtered.Body.String(), `"id":"slideshow-pptx"`) || strings.Contains(filtered.Body.String(), `"id":"report"`) {
		t.Fatalf("slideshow template filter: %d %s", filtered.Code, filtered.Body.String())
	}
	if got := request(t, svc, "GET", "/templates?type=unknown", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("bad filter: %d %s", got.Code, got.Body.String())
	}
	for _, test := range []struct{ body, wantType, wantTemplate string }{
		{`{"template":"report"}`, store.TypePDF, "report"},
		{`{"template":"slideshow-pptx"}`, store.TypeSlideshow, "slideshow-pptx"},
		{`{"type":"slideshow"}`, store.TypeSlideshow, "slideshow-pptx"},
	} {
		created := request(t, svc, "POST", "/docs", test.body)
		if created.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", test.body, created.Code, created.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response["type"] != test.wantType || response["template"] != test.wantTemplate {
			t.Fatalf("type metadata for %s: %#v", test.body, response)
		}
		if response["pdf_url"] == nil {
			t.Fatalf("missing pdf_url for %s: %#v", test.body, response)
		}
		_, hasPPTX := response["pptx_url"]
		if hasPPTX != (test.wantType == store.TypeSlideshow) {
			t.Fatalf("pptx_url for %s: %#v", test.body, response)
		}
		if test.wantType == store.TypeSlideshow {
			scene := request(t, svc, "GET", response["slides_url"].(string), "")
			var sceneJSON map[string]any
			if scene.Code != http.StatusOK || json.Unmarshal(scene.Body.Bytes(), &sceneJSON) != nil {
				t.Fatalf("slide scene: %d %s", scene.Code, scene.Body.String())
			}
			fonts, ok := sceneJSON["fonts"].(map[string]any)
			if !ok || fonts["regular_url"] != "/slides/fonts/regular.ttf" || fonts["bold_url"] != "/slides/fonts/bold.ttf" {
				t.Fatalf("scene fonts: %#v", sceneJSON["fonts"])
			}
			slides := sceneJSON["slides"].([]any)
			elements := slides[0].(map[string]any)["elements"].([]any)
			if elements[0].(map[string]any)["image_url"] != "/docs/"+response["doc_id"].(string)+"/images/photo.png?version=1" {
				t.Fatalf("image URL: %#v", elements[0])
			}
			pptxURL := response["pptx_url"].(string)
			pptx := request(t, svc, "GET", pptxURL, "")
			if pptx.Code != http.StatusOK || !bytes.HasPrefix(pptx.Body.Bytes(), []byte("PK")) || pptx.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.presentationml.presentation" {
				t.Fatalf("pptx download: %d %q %q", pptx.Code, pptx.Body.String(), pptx.Header().Get("Content-Type"))
			}
			id := response["doc_id"].(string)
			doc, err := st.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := driver.Delete(context.Background(), doc.Current().PPTXKey); err != nil {
				t.Fatal(err)
			}
			rebuilt := request(t, svc, "GET", pptxURL, "")
			if rebuilt.Code != http.StatusOK || !bytes.HasPrefix(rebuilt.Body.Bytes(), []byte("PK")) {
				t.Fatalf("rebuilt pptx: %d %q", rebuilt.Code, rebuilt.Body.String())
			}
		}
	}
	for _, body := range []string{`{"type":"vector"}`, `{"template":"report","type":"slideshow"}`} {
		if got := request(t, svc, "POST", "/docs", body); got.Code != http.StatusBadRequest {
			t.Fatalf("invalid type %s: %d %s", body, got.Code, got.Body.String())
		}
	}
}

func TestSlideshowPDFServesSavedOutputAndAttemptsLegacyRebuild(t *testing.T) {
	svc, driver, st := testService(t, "exit 1\n")
	doc, err := st.CreateProject("deck", "slideshow-pptx", "Deck", store.TypeSlideshow)
	if err != nil {
		t.Fatal(err)
	}
	pdfKey := "pdf/" + doc.ID + "/versions/1/pdf_render.pdf"
	if err := driver.Put(context.Background(), pdfKey, []byte("%PDF-legacy")); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordOutput(doc.ID, 1, store.Output{PDFKey: pdfKey, Manifest: `{"page_size":[1,1],"elements":[]}`}); err != nil {
		t.Fatal(err)
	}
	got := request(t, svc, "GET", "/docs/"+doc.ID+"/pdf?version=1", "")
	if got.Code != http.StatusOK || got.Body.String() != "%PDF-legacy" {
		t.Fatalf("saved PDF: %d %s", got.Code, got.Body.String())
	}
	if err := driver.Delete(context.Background(), pdfKey); err != nil {
		t.Fatal(err)
	}
	got = request(t, svc, "GET", "/docs/"+doc.ID+"/pdf?version=1", "")
	if got.Code != http.StatusInternalServerError || strings.Contains(got.Body.String(), "NO_PDF_OUTPUT") {
		t.Fatalf("legacy PDF rebuild response: %d %s", got.Code, got.Body.String())
	}
}

func TestTemplatesSeedOnlyOnce(t *testing.T) {
	svc, driver, _ := testService(t, validRender)
	if err := svc.SeedTemplates(); err != nil {
		t.Fatal(err)
	}
	key := "templates/report/template.json"
	custom := []byte(`{"id":"report","name":"Custom","script":"custom script"}`)
	if err := driver.Put(context.Background(), key, custom); err != nil {
		t.Fatal(err)
	}
	if err := driver.Delete(context.Background(), "templates/report/cover.png"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SeedTemplates(); err != nil {
		t.Fatal(err)
	}
	got, err := driver.Get(context.Background(), key)
	var seeded Template
	if err == nil {
		err = json.Unmarshal(got, &seeded)
	}
	if err != nil || seeded.Name != "Custom" || seeded.Script != "custom script" || seeded.CoverKey == "" {
		t.Fatalf("seed overwrote template: %s %v", got, err)
	}
	if cover, err := driver.Get(context.Background(), seeded.CoverKey); err != nil || len(cover) == 0 {
		t.Fatalf("custom template cover not backfilled: %d bytes, %v", len(cover), err)
	}
}

func TestLegacyVersionWithoutCoverKeyRegeneratesCover(t *testing.T) {
	svc, driver, st := testService(t, validRender)
	doc, err := st.Create("script")
	if err != nil {
		t.Fatal(err)
	}
	if result := svc.run(doc.ID, 1, false); !strings.Contains(resultText(result), `"ok": true`) {
		t.Fatalf("initial render: %s", resultText(result))
	}
	doc, err = st.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	coverKey := doc.Versions[0].CoverKey
	doc.Versions[0].CoverKey = ""
	prefix, err := store.ProjectPrefix(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Put(context.Background(), prefix+"/project.json", metadata); err != nil {
		t.Fatal(err)
	}
	if err := driver.Delete(context.Background(), coverKey); err != nil {
		t.Fatal(err)
	}
	got := request(t, svc, "GET", "/docs/"+doc.ID+"/cover?version=1", "")
	if got.Code != http.StatusOK || !strings.HasPrefix(got.Body.String(), "\x89PNG") {
		t.Fatalf("legacy cover: %d %x", got.Code, got.Body.Bytes())
	}
	updated, err := st.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Versions[0].CoverKey == "" {
		t.Fatal("legacy version cover key was not persisted")
	}
}

func TestCreateRollsBackFailedRender(t *testing.T) {
	svc, driver, _ := testService(t, "exit 1\n")
	if err := svc.SeedTemplates(); err != nil {
		t.Fatal(err)
	}
	got := request(t, svc, "POST", "/docs", `{"template":"empty"}`)
	if got.Code != 500 {
		t.Fatalf("create: %d %s", got.Code, got.Body.String())
	}
	keys, err := driver.List(context.Background(), "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("failed project remains: %v", keys)
	}
}

func TestMissingHistoricalPDFUsesAssetSnapshot(t *testing.T) {
	shell := "asset_dir=${PDF_ASSET_ROOT#file://}\ncat \"$asset_dir/accent.svg\" > \"$2\"\nprintf '%s' '{\"page_size\":[1,1],\"elements\":[]}' > \"$2.manifest.json\"\n"
	svc, driver, st := testService(t, shell)
	doc, err := st.Create("script")
	if err != nil {
		t.Fatal(err)
	}
	assetKey := "pdf/" + doc.ID + "/accent.svg"
	if err := driver.Put(context.Background(), assetKey, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if res := svc.run(doc.ID, 1, false); !strings.Contains(resultText(res), `"ok": true`) {
		t.Fatalf("render: %s", resultText(res))
	}
	if err := driver.Put(context.Background(), assetKey, []byte("second")); err != nil {
		t.Fatal(err)
	}
	pdfKey := "pdf/" + doc.ID + "/versions/1/pdf_render.pdf"
	if err := driver.Delete(context.Background(), pdfKey); err != nil {
		t.Fatal(err)
	}
	got := request(t, svc, "GET", "/docs/"+doc.ID+"/pdf?version=1", "")
	if got.Code != 200 || got.Body.String() != "first" {
		t.Fatalf("rebuild: %d %s", got.Code, got.Body.String())
	}
}
