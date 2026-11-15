// Package storage stores document assets (SVGs, images) and resolves them to
// URIs the renderer can load. File writes to local disk; S3 is a later driver.
package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Storage stores document assets and resolves them to loadable URIs.
// Implementations: File (local disk), S3 (later). The URI carries the scheme so
// the renderer knows how to load it (file:// vs s3://).
type Storage interface {
	// Write stores data at key (a doc-relative path) and returns its absolute
	// URI, e.g. "file:///abs/tmp/1/chart.svg" or "s3://bucket/1/chart.svg".
	Write(ctx context.Context, key string, data []byte) (string, error)
	// Read loads an asset back by key.
	Read(ctx context.Context, key string) ([]byte, error)
	// Root returns the asset root URI for a document.
	Root(docID string) string
}

// File is a local-disk Storage under a base directory. Assets live at
// <base>/<docID>/<key> and are exposed as file:// URIs.
type File struct {
	base string
}

// NewFile returns a File storage rooted at base (created on demand).
func NewFile(base string) *File {
	return &File{base: base}
}

// Root returns the file:// asset root URI for a document.
func (f *File) Root(docID string) string {
	dir, err := filepath.Abs(filepath.Join(f.base, docID))
	if err != nil {
		dir = filepath.Join(f.base, docID)
	}
	return "file://" + dir
}

// Write stores data under <base>/<key> and returns its file:// URI.
func (f *File) Write(_ context.Context, key string, data []byte) (string, error) {
	if !filepath.IsLocal(key) {
		return "", fmt.Errorf("invalid asset key %q", key)
	}
	path := filepath.Join(f.base, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return "file://" + abs, nil
}

// Read loads an asset back by key.
func (f *File) Read(_ context.Context, key string) ([]byte, error) {
	if !filepath.IsLocal(key) {
		return nil, fmt.Errorf("invalid asset key %q", key)
	}
	return os.ReadFile(filepath.Join(f.base, key))
}
