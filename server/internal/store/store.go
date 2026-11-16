// Package store holds document drafts and versions. FS is the active store;
// Memory supports isolated tests and Postgres remains a legacy implementation.
package store

import (
	"fmt"
	"sync"
	"time"
)

// ScriptVersion is one revision of a document's generating script and its
// rendered output.
type ScriptVersion struct {
	Version   int       `json:"version"`
	Script    string    `json:"script"`
	PDFKey    string    `json:"pdf_key,omitempty"`
	CoverKey  string    `json:"cover_key,omitempty"`
	PPTXKey   string    `json:"pptx_key,omitempty"`
	Manifest  string    `json:"manifest,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Document is a set of script versions plus a working draft. The latest
// version is the head.
type Document struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Template  string          `json:"template"`
	Type      string          `json:"type"`
	Ready     bool            `json:"ready"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Versions  []ScriptVersion `json:"versions"`
	Draft     string          `json:"draft"` // working script; committed on render
}

const (
	TypePDF       = "pdf"
	TypeSlideshow = "slideshow"
)

func NormalizeType(projectType string) string {
	if projectType == TypeSlideshow {
		return TypeSlideshow
	}
	return TypePDF
}

// Current returns the head version.
func (d Document) Current() ScriptVersion {
	return d.Versions[len(d.Versions)-1]
}

// Store is the document/version persistence interface.
type Store interface {
	Create(script string) (Document, error)
	CreateProject(script, template, title, projectType string) (Document, error)
	Seed(id, script string) (Document, error)
	Get(id string) (Document, error)
	List() ([]Document, error)
	Rename(id, title string) (Document, error)
	Delete(id string) error
	// Edit stores a draft without bumping the version; committed on Commit.
	Edit(id string, baseVersion int, script string) error
	// Commit appends the draft as a new head version.
	Commit(id string) (ScriptVersion, error)
	RecordOutput(id string, version int, pdfKey, coverKey, pptxKey, manifest string) error
	ListVersions(id string) ([]ScriptVersion, error)
	Restore(id string, version int) (ScriptVersion, error)
}

// Memory is a mutex-guarded in-memory Store.
type Memory struct {
	mu   sync.RWMutex
	docs map[string]*Document
	seq  int
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{docs: map[string]*Document{}}
}

// Create stores a new document seeded with script at version 1.
func (s *Memory) Create(script string) (Document, error) {
	return s.CreateProject(script, "", "Untitled", TypePDF)
}

func (s *Memory) CreateProject(script, template, title, projectType string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if projectType != TypePDF && projectType != TypeSlideshow {
		return Document{}, fmt.Errorf("invalid project type %q", projectType)
	}
	s.seq++
	id := fmt.Sprintf("doc-%d", s.seq)
	d := &Document{
		ID:    id,
		Title: title, Template: template, Type: projectType, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Versions: []ScriptVersion{{Version: 1, Script: script, CreatedAt: time.Now().UTC()}},
		Draft:    script,
	}
	s.docs[id] = d
	return *d, nil
}

// Seed stores a document under a fixed id (dev/testing). Runtime startup no
// longer creates demo documents; projects are created from templates.
func (s *Memory) Seed(id, script string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[id]; ok {
		return Document{}, fmt.Errorf("document %q already exists", id)
	}
	d := &Document{
		ID:    id,
		Title: id, Type: TypePDF, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Versions: []ScriptVersion{{Version: 1, Script: script, CreatedAt: time.Now().UTC()}},
		Draft:    script,
	}
	s.docs[id] = d
	return *d, nil
}

// Get returns a copy of a document.
func (s *Memory) Get(id string) (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.docs[id]
	if !ok {
		return Document{}, fmt.Errorf("document %q not found", id)
	}
	return clone(*d), nil
}

func (s *Memory) List() ([]Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Document, 0, len(s.docs))
	for _, d := range s.docs {
		out = append(out, clone(*d))
	}
	return out, nil
}

func (s *Memory) Rename(id, title string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[id]
	if !ok {
		return Document{}, fmt.Errorf("document %q not found", id)
	}
	d.Title, d.UpdatedAt = title, time.Now().UTC()
	return clone(*d), nil
}

func (s *Memory) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[id]; !ok {
		return fmt.Errorf("document %q not found", id)
	}
	delete(s.docs, id)
	return nil
}

// Edit stores a draft script without bumping the version. The draft is
// committed as a new version only on Commit (render). Rejects a stale base_version.
func (s *Memory) Edit(id string, baseVersion int, script string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[id]
	if !ok {
		return fmt.Errorf("document %q not found", id)
	}
	cur := d.Versions[len(d.Versions)-1]
	if baseVersion != cur.Version {
		return fmt.Errorf("stale base_version %d: current version is %d", baseVersion, cur.Version)
	}
	d.Draft = script
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// Commit appends the current draft as a new head version and returns it.
func (s *Memory) Commit(id string) (ScriptVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[id]
	if !ok {
		return ScriptVersion{}, fmt.Errorf("document %q not found", id)
	}
	cur := d.Versions[len(d.Versions)-1]
	v := ScriptVersion{Version: cur.Version + 1, Script: d.Draft, CreatedAt: time.Now().UTC()}
	d.Versions = append(d.Versions, v)
	d.UpdatedAt = time.Now().UTC()
	return v, nil
}

// RecordOutput attaches a rendered PDF + manifest to an existing version.
func (s *Memory) RecordOutput(id string, version int, pdfKey, coverKey, pptxKey, manifest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[id]
	if !ok {
		return fmt.Errorf("document %q not found", id)
	}
	for i := range d.Versions {
		if d.Versions[i].Version == version {
			d.Versions[i].PDFKey = pdfKey
			d.Versions[i].CoverKey = coverKey
			d.Versions[i].PPTXKey = pptxKey
			d.Versions[i].Manifest = manifest
			d.Ready = true
			d.UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return fmt.Errorf("version %d not found in document %q", version, id)
}

// ListVersions returns all versions, oldest first.
func (s *Memory) ListVersions(id string) ([]ScriptVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.docs[id]
	if !ok {
		return nil, fmt.Errorf("document %q not found", id)
	}
	return append([]ScriptVersion(nil), d.Versions...), nil
}

// Restore sets the draft to an older version's script (undo). The restored
// script is committed as a new version only on the next render.
func (s *Memory) Restore(id string, version int) (ScriptVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[id]
	if !ok {
		return ScriptVersion{}, fmt.Errorf("document %q not found", id)
	}
	var src *ScriptVersion
	for i := range d.Versions {
		if d.Versions[i].Version == version {
			src = &d.Versions[i]
			break
		}
	}
	if src == nil {
		return ScriptVersion{}, fmt.Errorf("version %d not found in document %q", version, id)
	}
	d.Draft = src.Script
	d.UpdatedAt = time.Now().UTC()
	return *src, nil
}

func clone(d Document) Document {
	d.Versions = append([]ScriptVersion(nil), d.Versions...)
	return d
}
