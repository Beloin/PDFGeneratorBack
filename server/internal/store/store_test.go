package store

import "testing"

func TestRenderedVersionSelectsNewestNativeOrLegacyOutput(t *testing.T) {
	t.Run("slideshow legacy output beats older native output", func(t *testing.T) {
		doc := Document{Type: TypeSlideshow, Versions: []ScriptVersion{
			{Version: 1, Manifest: "m1", CoverKey: "c1", PDFKey: "pdf1", PPTXKey: "p1", SlidesKey: "s1"},
			{Version: 2, Manifest: "m2", CoverKey: "c2", PPTXKey: "p2"},
			{Version: 3},
		}}
		version, ok := doc.RenderedVersion()
		if !ok || version.Version != 2 {
			t.Fatalf("rendered version = %+v, ok=%v", version, ok)
		}
	})
	t.Run("legacy PDF without cover remains visible", func(t *testing.T) {
		doc := Document{Type: TypePDF, Versions: []ScriptVersion{
			{Version: 1, Manifest: "m1", CoverKey: "c1", PDFKey: "p1"},
			{Version: 2, Manifest: "m2", PDFKey: "p2"},
			{Version: 3},
		}}
		version, ok := doc.RenderedVersion()
		if !ok || version.Version != 2 {
			t.Fatalf("rendered version = %+v, ok=%v", version, ok)
		}
	})
}
