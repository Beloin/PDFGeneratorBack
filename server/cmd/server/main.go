// Command server is the Go main server: source of truth for documents, script
// versions, and rendered outputs. It exposes the tools as MCP (streamable HTTP)
// and the same operations as REST.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/server"

	"pdfeditor/server/internal/runner"
	"pdfeditor/server/internal/service"
	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/store"
	"pdfeditor/server/internal/ws"
)

func main() {
	tmpDir := workspaceTmpDir()
	addr := flag.String("addr", ":8080", "listen address")
	genPath := flag.String("gen", "./gen/run.py", "path to gen/run.py")
	python := flag.String("python", "python3", "python interpreter for the runner")
	outDir := flag.String("out", filepath.Join(tmpDir, "pdfs"), "directory for rendered PDFs")
	assetsDir := flag.String("assets", filepath.Join(tmpDir, "assets"), "directory for document assets (SVGs, images)")
	flag.Parse()

	absOutDir, err := filepath.Abs(*outDir)
	if err != nil {
		log.Fatalf("resolve output dir: %v", err)
	}
	if err := os.MkdirAll(absOutDir, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	st := newStore()
	hub := ws.NewHub()
	assets := storage.NewFile(*assetsDir)
	svc := service.New(st, runner.New(*genPath, *python), hub, absOutDir, assets)

	// Dev: seed fixed documents the FE can rely on for testing. Skip if they
	// already persist in the database.
	for _, seed := range []struct{ id, tmpl string }{{"1", "report"}, {"2", "slideshow"}} {
		if _, ok := svc.CurrentVersion(seed.id); ok {
			continue
		}
		if err := svc.Seed(seed.id, seed.tmpl); err != nil {
			log.Fatalf("seed: %v", err)
		}
	}

	mcpSrv := server.NewMCPServer("pdf-editor", "0.1.0")
	svc.RegisterTools(mcpSrv)

	mux := http.NewServeMux()
	mux.Handle("/mcp", server.NewStreamableHTTPServer(mcpSrv))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/ws", hub.Handler(ws.Options{
		CanAccess:      func(_, _ string) bool { return true }, // dev: single user; ticket auth is Phase 6
		CurrentVersion: svc.CurrentVersion,
	}))
	svc.RegisterRoutes(mux)

	log.Printf("pdf-editor server listening on %s (MCP at /mcp, REST at /docs)", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func workspaceTmpDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "./tmp"
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".hephconfig")); err == nil {
			return filepath.Join(dir, "tmp")
		}
		if dir == filepath.Dir(dir) {
			return filepath.Join(wd, "tmp")
		}
	}
}

// newStore returns a Postgres store when the database is reachable, else an
// in-memory store (dev fallback).
func newStore() store.Store {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://pdfeditor:pdfeditor@localhost:5432/pdfeditor"
	}
	pg, err := store.NewPostgres(context.Background(), url)
	if err != nil {
		log.Printf("postgres unavailable (%v); using in-memory store", err)
		return store.NewMemory()
	}
	log.Printf("using postgres store")
	return pg
}
