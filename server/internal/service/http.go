package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/store"
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

func writeDocumentError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeError(w, status, err)
}

func projectResponse(d store.Document) map[string]any {
	v := d.Current().Version
	rendered := v
	for i := len(d.Versions) - 1; i >= 0; i-- {
		if d.Versions[i].Manifest != "" && d.Versions[i].PDFKey != "" {
			rendered = d.Versions[i].Version
			break
		}
	}
	response := map[string]any{
		"doc_id": d.ID, "title": d.Title, "template": d.Template,
		"type":    store.NormalizeType(d.Type),
		"version": v, "rendered_version": rendered, "created_at": d.CreatedAt, "updated_at": d.UpdatedAt,
		"pdf_url":      fmt.Sprintf("/docs/%s/pdf?version=%d", d.ID, rendered),
		"cover_url":    fmt.Sprintf("/docs/%s/cover?version=%d", d.ID, rendered),
		"manifest_url": fmt.Sprintf("/docs/%s/manifest?version=%d", d.ID, rendered),
	}
	if store.NormalizeType(d.Type) == store.TypeSlideshow {
		response["pptx_url"] = fmt.Sprintf("/docs/%s/pptx?version=%d", d.ID, rendered)
	}
	return response
}

func validTitle(title string) bool {
	return title != "" && utf8.RuneCountInString(title) <= 120
}

func (s *Service) handleTemplates(w http.ResponseWriter, r *http.Request) {
	typeFilter := r.URL.Query().Get("type")
	if typeFilter != "" && typeFilter != store.TypePDF && typeFilter != store.TypeSlideshow {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown template type %q", typeFilter))
		return
	}
	templates, err := s.listTemplates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]string, 0, len(templates))
	for _, t := range templates {
		projectType := store.NormalizeType(t.Type)
		if typeFilter != "" && projectType != typeFilter {
			continue
		}
		out = append(out, map[string]string{"id": t.ID, "name": t.Name, "type": projectType, "cover_url": "/templates/" + t.ID + "/cover"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Template string  `json:"template"`
		Title    *string `json:"title"`
		Type     string  `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Type != "" && body.Type != store.TypePDF && body.Type != store.TypeSlideshow {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown project type %q", body.Type))
		return
	}
	if body.Template == "" {
		if body.Type == store.TypeSlideshow {
			body.Template = "slideshow-pptx"
		} else {
			body.Template = "empty"
		}
	}
	if _, err := templateKey(body.Template); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	t, err := s.getTemplate(body.Template)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown template %q", body.Template))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	templateType := store.NormalizeType(t.Type)
	if body.Type != "" && body.Type != templateType {
		writeError(w, http.StatusBadRequest, fmt.Errorf("template %q has type %q, not %q", t.ID, templateType, body.Type))
		return
	}
	title := t.Name
	if body.Title != nil {
		title = strings.TrimSpace(*body.Title)
	}
	if !validTitle(title) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("title must be 1 to 120 characters"))
		return
	}
	doc, err := s.store.CreateProject(t.Script, t.ID, title, templateType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	res := s.run(doc.ID, 1, false)
	var render struct {
		OK        bool   `json:"ok"`
		Traceback string `json:"traceback"`
	}
	text := resultText(res)
	if err := json.Unmarshal([]byte(text), &render); err != nil || !render.OK {
		_ = s.store.Delete(doc.ID)
		if render.Traceback != "" {
			text = render.Traceback
		}
		writeError(w, http.StatusInternalServerError, fmt.Errorf("initial render failed: %s", text))
		return
	}
	doc, err = s.store.Get(doc.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, projectResponse(doc))
}

func (s *Service) handleListDocs(w http.ResponseWriter, _ *http.Request) {
	docs, err := s.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, projectResponse(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": out})
}

func (s *Service) handleGetDoc(w http.ResponseWriter, r *http.Request) {
	doc, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse(doc))
}

func (s *Service) handleRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if !validTitle(body.Title) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("title must be 1 to 120 characters"))
		return
	}
	doc, err := s.store.Rename(r.PathValue("id"), body.Title)
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse(doc))
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mu, _ := s.renderLocks.LoadOrStore(id, &sync.Mutex{})
	lock := mu.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if err := s.store.Delete(id); err != nil {
		writeDocumentError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleGetPDF(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	doc, err := s.store.Get(id)
	if err != nil {
		writeDocumentError(w, err)
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
		writeError(w, http.StatusNotFound, fmt.Errorf("no pdf for version %d", version))
		return
	}
	var pdf []byte
	if sv.PDFKey != "" {
		pdf, err = s.driver.Get(context.Background(), sv.PDFKey)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if pdf == nil {
		if sv.Manifest == "" {
			writeError(w, http.StatusNotFound, fmt.Errorf("no pdf for version %d", version))
			return
		}
		res := s.run(id, version, true)
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
		if !ok || sv.PDFKey == "" {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("rebuilt pdf for version %d has no key", version))
			return
		}
		pdf, err = s.driver.Get(context.Background(), sv.PDFKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("rebuilt pdf for version %d: %w", version, err))
			return
		}
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

func (s *Service) handleGetPPTX(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	doc, err := s.store.Get(id)
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	if store.NormalizeType(doc.Type) != store.TypeSlideshow {
		writeError(w, http.StatusNotFound, fmt.Errorf("project %q has no PowerPoint output", id))
		return
	}
	version := doc.Current().Version
	if v := r.URL.Query().Get("version"); v != "" {
		if n, parseErr := strconv.Atoi(v); parseErr == nil {
			version = n
		}
	}
	sv, ok := s.docVersion(doc, version)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no PowerPoint for version %d", version))
		return
	}
	var pptx []byte
	if sv.PPTXKey != "" {
		pptx, err = s.driver.Get(context.Background(), sv.PPTXKey)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if pptx == nil {
		if sv.Manifest == "" {
			writeError(w, http.StatusNotFound, fmt.Errorf("no PowerPoint for version %d", version))
			return
		}
		res := s.run(id, version, true)
		var rebuilt struct {
			OK        bool   `json:"ok"`
			Traceback string `json:"traceback"`
		}
		result := resultText(res)
		if json.Unmarshal([]byte(result), &rebuilt) != nil || !rebuilt.OK {
			if rebuilt.Traceback != "" {
				result = rebuilt.Traceback
			}
			writeError(w, http.StatusInternalServerError, fmt.Errorf("rebuild PowerPoint for version %d failed: %s", version, result))
			return
		}
		doc, err = s.store.Get(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		sv, ok = s.docVersion(doc, version)
		if !ok || sv.PPTXKey == "" {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("rebuilt PowerPoint for version %d has no key", version))
			return
		}
		pptx, err = s.driver.Get(context.Background(), sv.PPTXKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("rebuilt PowerPoint for version %d: %w", version, err))
			return
		}
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"presentation-%s-v%d.pptx\"", id, version))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pptx)
}

func (s *Service) handleGetCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	doc, err := s.store.Get(id)
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	version := doc.Current().Version
	if v := r.URL.Query().Get("version"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			version = n
		}
	}
	sv, ok := s.docVersion(doc, version)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no cover for version %d", version))
		return
	}
	var png []byte
	if sv.CoverKey != "" {
		png, err = s.driver.Get(context.Background(), sv.CoverKey)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			writeError(w, 500, err)
			return
		}
	}
	if png == nil && sv.Manifest != "" {
		res := s.run(id, version, true)
		var rebuilt struct {
			OK        bool   `json:"ok"`
			Traceback string `json:"traceback"`
		}
		if json.Unmarshal([]byte(resultText(res)), &rebuilt) != nil || !rebuilt.OK {
			writeError(w, 500, fmt.Errorf("rebuild cover for version %d failed: %s", version, rebuilt.Traceback))
			return
		}
		doc, err = s.store.Get(id)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		sv, ok = s.docVersion(doc, version)
		if !ok || sv.CoverKey == "" {
			writeError(w, 500, fmt.Errorf("rebuilt cover for version %d has no key", version))
			return
		}
		png, err = s.driver.Get(context.Background(), sv.CoverKey)
		if err != nil {
			writeError(w, 500, err)
			return
		}
	}
	if png == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no cover for version %d", version))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Service) handleGetTemplateCover(w http.ResponseWriter, r *http.Request) {
	t, err := s.getTemplate(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			writeError(w, 500, err)
		}
		return
	}
	if t.CoverKey == "" {
		http.NotFound(w, r)
		return
	}
	png, err := s.driver.Get(context.Background(), t.CoverKey)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Service) handleListVersions(w http.ResponseWriter, r *http.Request) {
	doc, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	vs, err := s.store.ListVersions(r.PathValue("id"))
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(vs))
	for _, v := range vs {
		item := map[string]any{
			"version": v.Version, "script": v.Script, "pdf_key": v.PDFKey,
			"cover_key": v.CoverKey, "pptx_key": v.PPTXKey, "manifest": v.Manifest, "created_at": v.CreatedAt,
			"pdf_url":   fmt.Sprintf("/docs/%s/pdf?version=%d", r.PathValue("id"), v.Version),
			"cover_url": fmt.Sprintf("/docs/%s/cover?version=%d", r.PathValue("id"), v.Version),
		}
		if store.NormalizeType(doc.Type) == store.TypeSlideshow {
			item["pptx_url"] = fmt.Sprintf("/docs/%s/pptx?version=%d", doc.ID, v.Version)
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc_id": r.PathValue("id"), "versions": items})
}

func (s *Service) handleListElements(w http.ResponseWriter, r *http.Request) {
	doc, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	version := doc.Current().Version
	if v := r.URL.Query().Get("version"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			version = n
		}
	}
	m, err := s.manifestFor(doc, version)
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
		writeError(w, http.StatusBadRequest, err)
		return
	}
	v, err := s.store.Restore(r.PathValue("id"), body.Version)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
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
	mux.HandleFunc("GET /templates", auth(s.handleTemplates))
	mux.HandleFunc("GET /templates/{id}/cover", auth(s.handleGetTemplateCover))
	mux.HandleFunc("POST /docs", auth(s.handleCreate))
	mux.HandleFunc("GET /docs", auth(s.handleListDocs))
	mux.HandleFunc("GET /docs/{id}", auth(s.handleGetDoc))
	mux.HandleFunc("PATCH /docs/{id}", auth(s.handleRename))
	mux.HandleFunc("DELETE /docs/{id}", auth(s.handleDelete))
	mux.HandleFunc("GET /docs/{id}/pdf", auth(s.handleGetPDF))
	mux.HandleFunc("GET /docs/{id}/pptx", auth(s.handleGetPPTX))
	mux.HandleFunc("GET /docs/{id}/cover", auth(s.handleGetCover))
	mux.HandleFunc("GET /docs/{id}/versions", auth(s.handleListVersions))
	mux.HandleFunc("GET /docs/{id}/manifest", auth(s.handleListElements))
	mux.HandleFunc("POST /docs/{id}/restore", auth(s.handleRestore))
	s.RegisterUploadRoutes(mux)
}
