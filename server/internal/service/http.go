package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes {"error": ..., "code": ...}, reading the code from a
// *CodedError and falling back to ErrError.
func writeError(w http.ResponseWriter, status int, err error) {
	code := ErrError
	if ce, ok := err.(*CodedError); ok {
		code = ce.Code
	}
	writeJSON(w, status, map[string]any{"error": err.Error(), "code": code})
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Template string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	script, ok := Templates[body.Template]
	if body.Template == "" {
		script = Templates["empty"]
	} else if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("unknown template %q", body.Template)})
		return
	}
	doc, err := s.store.Create(script)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"doc_id": doc.ID, "version": 1})
}

func (s *Service) handleGetPDF(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	doc, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	version := doc.Current().Version
	if v := r.URL.Query().Get("version"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			version = n
		}
	}
	sv, ok := s.docVersion(doc, version)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("no pdf for version %d", version)})
		return
	}
	missing := sv.PDFPath == ""
	if !missing {
		_, err = os.Stat(sv.PDFPath)
		missing = errors.Is(err, os.ErrNotExist)
		if err != nil && !missing {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if missing {
		if sv.Manifest == "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("no pdf for version %d", version)})
			return
		}
		res := s.run(id, version)
		var rebuild struct {
			OK        bool   `json:"ok"`
			Traceback string `json:"traceback"`
		}
		result := resultText(res)
		if err := json.Unmarshal([]byte(result), &rebuild); err != nil || !rebuild.OK {
			if rebuild.Traceback != "" {
				result = rebuild.Traceback
			}
			writeError(w, http.StatusInternalServerError, fmt.Errorf("rebuild pdf for version %d failed: %s", version, result))
			return
		}
		doc, err = s.store.Get(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		sv, ok = s.docVersion(doc, version)
		if !ok || sv.PDFPath == "" {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("rebuild pdf for version %d failed: %s", version, resultText(res)))
			return
		}
		if _, err := os.Stat(sv.PDFPath); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("rebuilt pdf for version %d: %w", version, err))
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, sv.PDFPath)
}

func (s *Service) handleListVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.store.ListVersions(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc_id": r.PathValue("id"), "versions": vs})
}

func (s *Service) handleListElements(w http.ResponseWriter, r *http.Request) {
	doc, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	m, err := s.manifestFor(doc, doc.Current().Version)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc_id": doc.ID, "page_size": m.PageSize, "elements": m.Elements})
}

func (s *Service) handleRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	v, err := s.store.Restore(r.PathValue("id"), body.Version)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc_id": r.PathValue("id"), "version": v.Version, "committed": false})
}

// auth is a minimal bearer-token check. Disabled when AUTH_TOKEN is unset
// (single-user dev). Real per-user auth is a Phase 6 prerequisite.
func auth(next http.HandlerFunc) http.HandlerFunc {
	token := os.Getenv("AUTH_TOKEN")
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// RegisterRoutes adds the REST routes (document operations; script text is
// only ever touched over MCP) to a mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /docs", auth(s.handleCreate))
	mux.HandleFunc("GET /docs/{id}/pdf", auth(s.handleGetPDF))
	mux.HandleFunc("GET /docs/{id}/versions", auth(s.handleListVersions))
	mux.HandleFunc("GET /docs/{id}/manifest", auth(s.handleListElements))
	mux.HandleFunc("POST /docs/{id}/restore", auth(s.handleRestore))
}
