package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestOpenRequiresMarker(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenFolder(dir); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("want ErrNotInitialized, got %v", err)
	}
	if _, err := OpenFolder(filepath.Join(dir, "missing")); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("want ErrNotInitialized for unmounted path, got %v", err)
	}
	if _, err := InitFolder(filepath.Join(dir, "no", "parent")); err == nil {
		t.Fatal("init must refuse when parent does not exist")
	}
	if _, err := InitFolder(filepath.Join(dir, "s")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFolder(filepath.Join(dir, "s")); err != nil {
		t.Fatal(err)
	}
}

func TestObjectsAndSnapshots(t *testing.T) {
	ctx := context.Background()
	f, err := InitFolder(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("WeakAurasSaved = {}\n")
	h := sum(data)
	if ok, _ := f.HasObject(ctx, h); ok {
		t.Fatal("unexpected object")
	}
	if err := f.PutObject(ctx, h, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := f.PutObject(ctx, h, bytes.NewReader(data)); err != nil {
		t.Fatalf("second put of same object: %v", err)
	}
	if err := f.PutObject(ctx, sum([]byte("other")), bytes.NewReader(data)); err == nil {
		t.Fatal("hash mismatch must fail")
	}
	rc, err := f.GetObject(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get object: %q %v", got, err)
	}

	if s, err := f.Latest(ctx); err != nil || s != nil {
		t.Fatalf("empty store latest: %v %v", s, err)
	}
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s1 := &Snapshot{Format: FormatVersion, ID: NewID(t0, "Mac Book"), Machine: "Mac Book", Created: t0,
		Files: []FileEntry{{Path: "retail/WTF/x.lua", Hash: h, Size: int64(len(data))}}}
	s2 := &Snapshot{Format: FormatVersion, ID: NewID(t0.Add(time.Second), "PC"), Parent: s1.ID, Machine: "PC", Created: t0.Add(time.Second)}
	for _, s := range []*Snapshot{s2, s1} {
		if err := f.Put(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Put(ctx, s1); err == nil {
		t.Fatal("overwriting a snapshot must fail")
	}
	// Junk files are ignored.
	os.WriteFile(filepath.Join(f.root, snapshotsDir, "x (conflicted copy).json.gz"), []byte("junk"), 0o644)
	list, err := f.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != s1.ID || list[1].Machine != "PC" {
		t.Fatalf("list: %+v %v", list, err)
	}
	latest, err := f.Latest(ctx)
	if err != nil || latest.ID != s2.ID || latest.Parent != s1.ID {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	got1, err := f.Get(ctx, s1.ID)
	if err != nil || len(got1.Files) != 1 || got1.Files[0].Hash != h {
		t.Fatalf("get: %+v %v", got1, err)
	}
	if !strings.HasPrefix(s1.ID, "20261110T120000.000Z_Mac-Book_") {
		t.Fatalf("id format: %s", s1.ID)
	}
}

func TestCorruptObjectDetected(t *testing.T) {
	ctx := context.Background()
	f, _ := InitFolder(filepath.Join(t.TempDir(), "store"))
	data := []byte("hello")
	h := sum(data)
	if err := f.PutObject(ctx, h, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	// Replace the object with valid gzip of other content.
	other := []byte("HELLO")
	oh := sum(other)
	f.PutObject(ctx, oh, bytes.NewReader(other))
	p, _ := f.objPath(h)
	op, _ := f.objPath(oh)
	raw, _ := os.ReadFile(op)
	os.WriteFile(p, raw, 0o644)
	rc, err := f.GetObject(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("corruption not detected")
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	f, _ := InitFolder(filepath.Join(t.TempDir(), "store"))
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var hashes []string
	for i := 0; i < 4; i++ {
		data := []byte{byte('a' + i)}
		h := sum(data)
		hashes = append(hashes, h)
		f.PutObject(ctx, h, bytes.NewReader(data))
		f.Put(ctx, &Snapshot{Format: FormatVersion, ID: NewID(t0.Add(time.Duration(i)*time.Minute), "m"),
			Files: []FileEntry{{Path: "retail/WTF/x", Hash: h, Size: 1}}})
	}
	// Age every object so prune may remove unreferenced ones.
	old := time.Now().Add(-48 * time.Hour)
	for _, h := range hashes {
		p, _ := f.objPath(h)
		os.Chtimes(p, old, old)
	}
	res, err := f.Prune(ctx, 2, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.SnapshotsRemoved != 2 || res.ObjectsRemoved != 2 {
		t.Fatalf("prune: %+v", res)
	}
	for i, h := range hashes {
		ok, _ := f.HasObject(ctx, h)
		if ok != (i >= 2) {
			t.Errorf("object %d present=%v", i, ok)
		}
	}
}

func TestSanitizeMachine(t *testing.T) {
	if got := SanitizeMachine("Ole's MacBook_Pro"); got != "Ole-s-MacBook-Pro" {
		t.Fatal(got)
	}
	if SanitizeMachine("___") != "machine" {
		t.Fatal("empty fallback")
	}
}
