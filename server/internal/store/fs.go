package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"pdfeditor/server/internal/storage"
)

var ErrNotFound = errors.New("document not found")

// FS keeps each document under one pdf/{id}/ key prefix. The driver decides
// whether those keys are local files or objects in a remote store.
type FS struct {
	mu     sync.Mutex
	driver storage.Driver
}

func NewFS(driver storage.Driver) *FS { return &FS{driver: driver} }

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func ProjectPrefix(id string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("invalid document id %q", id)
	}
	return path.Join("pdf", id), nil
}

func VersionPrefix(id string, version int) (string, error) {
	prefix, err := ProjectPrefix(id)
	if err != nil {
		return "", err
	}
	if version < 1 {
		return "", fmt.Errorf("invalid version %d", version)
	}
	return path.Join(prefix, "versions", fmt.Sprint(version)), nil
}

func (s *FS) read(id string) (Document, error) {
	prefix, err := ProjectPrefix(id)
	if err != nil {
		return Document{}, err
	}
	b, err := s.driver.Get(context.Background(), path.Join(prefix, "project.json"))
	if errors.Is(err, storage.ErrNotFound) {
		return Document{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err != nil {
		return Document{}, err
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		return Document{}, fmt.Errorf("decode %s: %w", id, err)
	}
	if doc.Type == "" {
		doc.Type = TypePDF
	}
	if len(doc.Versions) == 0 {
		return Document{}, fmt.Errorf("document %q has no versions", id)
	}
	return doc, nil
}

func (s *FS) write(doc Document) error {
	prefix, err := ProjectPrefix(doc.ID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return s.driver.Put(context.Background(), path.Join(prefix, "project.json"), b)
}

func (s *FS) Create(script string) (Document, error) {
	return s.CreateProject(script, "", "Untitled", TypePDF)
}

func (s *FS) CreateProject(script, template, title, projectType string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Document{}, err
	}
	id := hex.EncodeToString(raw[:])
	if projectType != TypePDF && projectType != TypeSlideshow {
		return Document{}, fmt.Errorf("invalid project type %q", projectType)
	}
	now := time.Now().UTC()
	doc := Document{ID: id, Title: title, Template: template, Type: projectType, CreatedAt: now, UpdatedAt: now,
		Draft: script, Versions: []ScriptVersion{{Version: 1, Script: script, CreatedAt: now}}}
	vp, _ := VersionPrefix(id, 1)
	if err := s.driver.Put(context.Background(), path.Join(vp, "script.py"), []byte(script)); err != nil {
		return Document{}, err
	}
	if err := s.write(doc); err != nil {
		_ = s.driver.DeletePrefix(context.Background(), path.Join("pdf", id))
		return Document{}, err
	}
	return doc, nil
}

func (s *FS) Seed(id, script string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.read(id); err == nil {
		return Document{}, fmt.Errorf("document %q already exists", id)
	} else if !errors.Is(err, ErrNotFound) {
		return Document{}, err
	}
	now := time.Now().UTC()
	doc := Document{ID: id, Title: id, Type: TypePDF, CreatedAt: now, UpdatedAt: now, Draft: script,
		Versions: []ScriptVersion{{Version: 1, Script: script, CreatedAt: now}}}
	vp, _ := VersionPrefix(id, 1)
	if err := s.driver.Put(context.Background(), path.Join(vp, "script.py"), []byte(script)); err != nil {
		return Document{}, err
	}
	if err := s.write(doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func (s *FS) Get(id string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read(id)
	return clone(d), err
}

func (s *FS) List() ([]Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.driver.List(context.Background(), "pdf")
	if err != nil {
		return nil, err
	}
	var out []Document
	for _, key := range keys {
		parts := strings.Split(key, "/")
		if len(parts) != 3 || parts[0] != "pdf" || parts[2] != "project.json" {
			continue
		}
		d, err := s.read(parts[1])
		if err != nil {
			return nil, err
		}
		if !d.Ready {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s *FS) Rename(id, title string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read(id)
	if err != nil {
		return Document{}, err
	}
	d.Title, d.UpdatedAt = title, time.Now().UTC()
	if err := s.write(d); err != nil {
		return Document{}, err
	}
	return d, nil
}

func (s *FS) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.read(id); err != nil {
		return err
	}
	prefix, _ := ProjectPrefix(id)
	return s.driver.DeletePrefix(context.Background(), prefix)
}

func (s *FS) Edit(id string, baseVersion int, script string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read(id)
	if err != nil {
		return err
	}
	cur := d.Current().Version
	if baseVersion != cur {
		return fmt.Errorf("stale base_version %d: current version is %d", baseVersion, cur)
	}
	d.Draft, d.UpdatedAt = script, time.Now().UTC()
	return s.write(d)
}

func (s *FS) Commit(id string) (ScriptVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read(id)
	if err != nil {
		return ScriptVersion{}, err
	}
	v := ScriptVersion{Version: d.Current().Version + 1, Script: d.Draft, CreatedAt: time.Now().UTC()}
	vp, _ := VersionPrefix(id, v.Version)
	if err := s.driver.Put(context.Background(), path.Join(vp, "script.py"), []byte(v.Script)); err != nil {
		return ScriptVersion{}, err
	}
	d.Versions = append(d.Versions, v)
	d.UpdatedAt = v.CreatedAt
	if err := s.write(d); err != nil {
		return ScriptVersion{}, err
	}
	return v, nil
}

func (s *FS) RecordOutput(id string, version int, pdfKey, coverKey, pptxKey, manifest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read(id)
	if err != nil {
		return err
	}
	vp, err := VersionPrefix(id, version)
	if err != nil {
		return err
	}
	index := -1
	for i := range d.Versions {
		if d.Versions[i].Version == version {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("version %d not found in document %q", version, id)
	}
	if err := s.driver.Put(context.Background(), path.Join(vp, "manifest.json"), []byte(manifest)); err != nil {
		return err
	}
	d.Versions[index].PDFKey, d.Versions[index].CoverKey, d.Versions[index].PPTXKey, d.Versions[index].Manifest = pdfKey, coverKey, pptxKey, manifest
	d.Ready = true
	d.UpdatedAt = time.Now().UTC()
	return s.write(d)
}

func (s *FS) ListVersions(id string) ([]ScriptVersion, error) {
	d, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	return append([]ScriptVersion(nil), d.Versions...), nil
}

func (s *FS) Restore(id string, version int) (ScriptVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read(id)
	if err != nil {
		return ScriptVersion{}, err
	}
	for _, v := range d.Versions {
		if v.Version == version {
			d.Draft, d.UpdatedAt = v.Script, time.Now().UTC()
			if err := s.write(d); err != nil {
				return ScriptVersion{}, err
			}
			return v, nil
		}
	}
	return ScriptVersion{}, fmt.Errorf("version %d not found in document %q", version, id)
}
