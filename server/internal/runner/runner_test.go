package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunResolvesRelativeRunnerPathBeforeChangingDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "render.sh")
	if err := os.WriteFile(path, []byte("printf '%s' '%PDF-1.4' > \"$2\"\nprintf '%s' '{\"page_size\":[1,1],\"elements\":[]}' > \"$2.manifest.json\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	result, err := New("render.sh", "sh").Run("script", "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("render failed: %s", result.Traceback)
	}
}
