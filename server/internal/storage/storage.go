// Package storage provides a key-based driver for project files. DriverFS is
// local storage; another driver can use the same keys in an object store.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrNotFound = errors.New("storage key not found")

type Driver interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte) error
	PutIfAbsent(context.Context, string, []byte) (bool, error)
	List(context.Context, string) ([]string, error)
	Delete(context.Context, string) error
	DeletePrefix(context.Context, string) error
}

type DriverFS struct{ root string }

func NewDriverFS(root string) (*DriverFS, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(root, ".write-check-*")
	if err != nil {
		return nil, fmt.Errorf("storage root %s is not writable: %w", root, err)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return &DriverFS{root: root}, nil
}

func (d *DriverFS) path(key string) (string, error) {
	if key == "" || key == "." || !filepath.IsLocal(key) || strings.Contains(key, "\\") {
		return "", fmt.Errorf("invalid storage key %q", key)
	}
	return filepath.Join(d.root, filepath.FromSlash(key)), nil
}

func (d *DriverFS) Get(_ context.Context, key string) ([]byte, error) {
	path, err := d.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func (d *DriverFS) Put(_ context.Context, key string, data []byte) error {
	path, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".put-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (d *DriverFS) PutIfAbsent(_ context.Context, key string, data []byte) (bool, error) {
	path, err := d.path(key)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".seed-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	if err = os.Link(f.Name(), path); errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (d *DriverFS) List(_ context.Context, prefix string) ([]string, error) {
	path, err := d.path(prefix)
	if err != nil {
		return nil, err
	}
	var keys []string
	err = filepath.WalkDir(path, func(p string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(d.root, p)
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	sort.Strings(keys)
	return keys, err
}

func (d *DriverFS) Delete(_ context.Context, key string) error {
	path, err := d.path(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (d *DriverFS) DeletePrefix(_ context.Context, prefix string) error {
	path, err := d.path(prefix)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}
