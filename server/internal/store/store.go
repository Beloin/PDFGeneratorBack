// Package store is the document/version store. Memory is the in-process
// implementation; Postgres persists to the shared database. The Service only
// talks to the Store interface.
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
	PDFPath   string    `json:"pdf_path,omitempty"`
	Manifest  string    `json:"manifest,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Document is a set of script versions plus a working draft. The latest
// version is the head.
type Document struct {
	ID       string          `json:"id"`
	Versions []ScriptVersion `json:"versions"`
	Draft    string          `json:"draft"` // working script; committed on render
}

// Current returns the head version.
func (d Document) Current() ScriptVersion {
	return d.Versions[len(d.Versions)-1]
}

// Store is the document/version persistence interface.
type Store interface {
	Create(script string) (Document, error)
	Seed(id, script string) (Document, error)
	Get(id string) (Document, error)
	// Edit stores a draft without bumping the version; committed on Commit.
	Edit(id string, baseVersion int, script string) error
	// Commit appends the draft as a new head version.
	Commit(id string) (ScriptVersion, error)
	RecordOutput(id string, version int, pdfPath, manifest string) error
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("doc-%d", s.seq)
	d := &Document{
		ID:       id,
		Versions: []ScriptVersion{{Version: 1, Script: script, CreatedAt: time.Now().UTC()}},
		Draft:    script,
	}
	s.docs[id] = d
	return *d, nil
}

// Seed stores a document under a fixed id (dev/testing). The FE relies on a
// seeded document while the real creation flow is being built.
func (s *Memory) Seed(id, script string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[id]; ok {
		return Document{}, fmt.Errorf("document %q already exists", id)
	}
	d := &Document{
		ID:       id,
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
	return v, nil
}

// RecordOutput attaches a rendered PDF + manifest to an existing version.
func (s *Memory) RecordOutput(id string, version int, pdfPath, manifest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[id]
	if !ok {
		return fmt.Errorf("document %q not found", id)
	}
	for i := range d.Versions {
		if d.Versions[i].Version == version {
			d.Versions[i].PDFPath = pdfPath
			d.Versions[i].Manifest = manifest
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
	return *src, nil
}

func clone(d Document) Document {
	d.Versions = append([]ScriptVersion(nil), d.Versions...)
	return d
}
