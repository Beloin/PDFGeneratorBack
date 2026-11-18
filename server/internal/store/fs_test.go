package store

import (
	"context"
	"errors"
	"testing"

	"pdfeditor/server/internal/storage"
)

func TestFSRestartAndStaleEdit(t *testing.T) {
	root := t.TempDir()
	driver, err := storage.NewDriverFS(root)
	if err != nil {
		t.Fatal(err)
	}
	first := NewFS(driver)
	doc, err := first.CreateProject("original", "report", "Report", TypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Edit(doc.ID, 2, "wrong"); err == nil {
		t.Fatal("stale edit accepted")
	}
	if err := first.Edit(doc.ID, 1, "updated"); err != nil {
		t.Fatal(err)
	}
	v, err := first.Commit(doc.ID)
	if err != nil || v.Version != 2 {
		t.Fatalf("commit: %+v %v", v, err)
	}
	if err := first.RecordOutput(doc.ID, 2, Output{PDFKey: "pdf/" + doc.ID + "/versions/2/pdf_render.pdf", CoverKey: "pdf/" + doc.ID + "/versions/2/cover.png", Manifest: `{"page_size":[1,1],"elements":[]}`}); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.NewDriverFS(root)
	if err != nil {
		t.Fatal(err)
	}
	second := NewFS(reopened)
	loaded, err := second.Get(doc.ID)
	if err != nil || loaded.Title != "Report" || loaded.Current().Script != "updated" || loaded.Current().Version != 2 {
		t.Fatalf("restart: %+v %v", loaded, err)
	}
	listed, err := second.List()
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v %v", listed, err)
	}
	if err := second.Delete(doc.ID); err != nil {
		t.Fatal(err)
	}
	_, err = second.Get(doc.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted project: %v", err)
	}
}

func TestFSProjectTypeAndPPTXKeyPersist(t *testing.T) {
	driver, err := storage.NewDriverFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := NewFS(driver)
	doc, err := first.CreateProject("slides", "slideshow-pptx", "Deck", TypeSlideshow)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RecordOutput(doc.ID, 1, Output{PDFKey: "pdf/key", CoverKey: "cover/key", PPTXKey: "pptx/key", SlidesKey: "slides/key", Manifest: `{"page_size":[1,1],"elements":[]}`}); err != nil {
		t.Fatal(err)
	}
	reopened := NewFS(driver)
	loaded, err := reopened.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Type != TypeSlideshow || loaded.Current().PPTXKey != "pptx/key" || loaded.Current().SlidesKey != "slides/key" {
		t.Fatalf("persisted project metadata: %+v", loaded)
	}
}

func TestRecordOutputPreservesLegacyPDFWhenBackfillingSlides(t *testing.T) {
	driver, err := storage.NewDriverFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st := NewFS(driver)
	doc, err := st.CreateProject("deck", "legacy-slides", "Deck", TypeSlideshow)
	if err != nil {
		t.Fatal(err)
	}
	legacyPDF := "pdf/" + doc.ID + "/versions/1/pdf_render.pdf"
	if err := st.RecordOutput(doc.ID, 1, Output{PDFKey: legacyPDF, CoverKey: "cover", PPTXKey: "pptx", Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordOutput(doc.ID, 1, Output{CoverKey: "cover", PPTXKey: "pptx", SlidesKey: "slides", Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}
	updated, err := st.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Current().PDFKey != legacyPDF || updated.Current().SlidesKey != "slides" {
		t.Fatalf("legacy output after backfill: %+v", updated.Current())
	}
}

func TestFSLegacyProjectDefaultsToPDF(t *testing.T) {
	driver, err := storage.NewDriverFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := NewFS(driver)
	doc, err := store.Create("legacy")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := ProjectPrefix(doc.ID)
	legacyJSON := []byte(`{"id":"` + doc.ID + `","title":"legacy","versions":[{"version":1,"script":"legacy"}],"draft":"legacy"}`)
	if err := driver.Put(context.Background(), prefix+"/project.json", legacyJSON); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Type != TypePDF {
		t.Fatalf("legacy type = %q, want pdf", loaded.Type)
	}
}
