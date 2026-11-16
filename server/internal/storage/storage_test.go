package storage

import (
	"context"
	"errors"
	"testing"
)

func TestDriverFSKeysAndSeedOnce(t *testing.T) {
	d, err := NewDriverFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := d.PutIfAbsent(ctx, "../escape", []byte("bad")); err == nil {
		t.Fatal("traversal key accepted")
	}
	created, err := d.PutIfAbsent(ctx, "templates/report/template.json", []byte("first"))
	if err != nil || !created {
		t.Fatalf("first seed: %v %v", created, err)
	}
	created, err = d.PutIfAbsent(ctx, "templates/report/template.json", []byte("second"))
	if err != nil || created {
		t.Fatalf("second seed: %v %v", created, err)
	}
	b, err := d.Get(ctx, "templates/report/template.json")
	if err != nil || string(b) != "first" {
		t.Fatalf("seed contents: %q %v", b, err)
	}
	keys, err := d.List(ctx, "templates")
	if err != nil || len(keys) != 1 || keys[0] != "templates/report/template.json" {
		t.Fatalf("keys: %v %v", keys, err)
	}
	if err := d.DeletePrefix(ctx, "templates/report"); err != nil {
		t.Fatal(err)
	}
	_, err = d.Get(ctx, "templates/report/template.json")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted key: %v", err)
	}
}
