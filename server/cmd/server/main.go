// Command server is the Go main server: source of truth for documents, script
// versions, and rendered outputs. It exposes the tools as MCP (streamable HTTP)
// and the same operations as REST.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"pdfeditor/server/internal/runner"
	"pdfeditor/server/internal/service"
	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/store"
	"pdfeditor/server/internal/ws"
)

func main() {
	addr := flag.String("addr", envOr("PDF_LISTEN_ADDR", ":8080"), "listen address")
	genPath := flag.String("gen", envOr("PDF_GEN_PATH", "./gen/run.py"), "path to gen/run.py")
	python := flag.String("python", envOr("PDF_PYTHON", "python3"), "python interpreter for the runner")
	dataDir := flag.String("data-dir", envOr("PDF_DATA_DIR", "../tmp"), "root for projects and templates")
	flag.Parse()

	driver, err := storage.NewDriverFS(*dataDir)
	if err != nil {
		log.Fatalf("open filesystem driver: %v", err)
	}
	st := store.NewFS(driver)
	hub := ws.NewHub()
	svc := service.New(st, runner.New(*genPath, *python), hub, driver)
	if err := svc.SeedTemplates(); err != nil {
		log.Fatalf("seed templates: %v", err)
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

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
