package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceTmpDirFromHephSandbox(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".hephconfig"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sandbox := filepath.Join(root, ".heph", "sandbox", "server", "__target_run", "ws", "server")
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sandbox)
	if got, want := workspaceTmpDir(), filepath.Join(root, "tmp"); got != want {
		t.Fatalf("tmp dir = %q, want %q", got, want)
	}
}
