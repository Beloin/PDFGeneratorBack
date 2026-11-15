package service

import (
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

func TestGetPDFRebuildsMissingFileWithManifest(t *testing.T) {
	for _, initialPath := range []string{"", "missing.pdf"} {
		t.Run(initialPath, func(t *testing.T) {
			dir := t.TempDir()
			outDir := filepath.Join(dir, "out")
			if err := os.Mkdir(outDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if initialPath != "" {
				if err := os.Remove(outDir); err != nil {
					t.Fatal(err)
				}
			}
			fakeRunner := filepath.Join(dir, "render.sh")
			if err := os.WriteFile(fakeRunner, []byte("printf '%s' '%PDF-1.4' > \"$2\"\nprintf '%s' '{\"page_size\":[1,1],\"elements\":[]}' > \"$2.manifest.json\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			st := store.NewMemory()
			doc, err := st.Create("script")
			if err != nil {
				t.Fatal(err)
			}
			pdfPath := initialPath
			if pdfPath != "" {
				pdfPath = filepath.Join(dir, pdfPath)
			}
			if err := st.RecordOutput(doc.ID, 1, pdfPath, `{"page_size":[1,1],"elements":[]}`); err != nil {
				t.Fatal(err)
			}
			svc := New(st, runner.New(fakeRunner, "sh"), ws.NewHub(), outDir, storage.NewFile(filepath.Join(dir, "assets")))
			req := httptest.NewRequest(http.MethodGet, "/docs/"+doc.ID+"/pdf", nil)
			req.SetPathValue("id", doc.ID)
			w := httptest.NewRecorder()
			svc.handleGetPDF(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if got := w.Body.String(); got != "%PDF-1.4" {
				t.Fatalf("PDF = %q", got)
			}
			stored, err := st.Get(doc.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored.Versions) != 1 {
				t.Fatalf("versions = %d", len(stored.Versions))
			}
			if _, err := os.Stat(stored.Current().PDFPath); err != nil {
				t.Fatalf("rebuilt PDF: %v", err)
			}
		})
	}
}

func TestGetPDFReportsRebuildFailureInsteadOfStalePath(t *testing.T) {
	dir := t.TempDir()
	st := store.NewMemory()
	doc, err := st.Create("script")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordOutput(doc.ID, 1, filepath.Join(dir, "gone.pdf"), `{"page_size":[1,1],"elements":[]}`); err != nil {
		t.Fatal(err)
	}
	svc := New(st, runner.New(filepath.Join(dir, "missing-runner.sh"), "sh"), ws.NewHub(), dir, storage.NewFile(filepath.Join(dir, "assets")))
	req := httptest.NewRequest(http.MethodGet, "/docs/"+doc.ID+"/pdf", nil)
	req.SetPathValue("id", doc.ID)
	w := httptest.NewRecorder()
	svc.handleGetPDF(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "missing-runner.sh") || strings.Contains(body, "stat ") {
		t.Fatalf("hidden rebuild failure: %s", body)
	}
}
