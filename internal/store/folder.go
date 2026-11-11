package store

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	markerName   = "wowsync-store.json"
	snapshotsDir = "snapshots"
	objectsDir   = "objects"
	snapExt      = ".json.gz"
	tmpPrefix    = ".tmp-"
)

type marker struct {
	Format  int       `json:"format"`
	Created time.Time `json:"created"`
	App     string    `json:"app"`
}

// Folder is a store in a plain directory: a NAS share, an external disk
// or a cloud-synced folder.
//
//	wowsync-store.json            marker, written once
//	snapshots/<id>.json.gz        manifests, immutable
//	objects/<ab>/<sha256>         gzip-compressed file content, immutable
type Folder struct {
	root string
}

var _ Store = (*Folder)(nil)

// OpenFolder opens an existing store. It returns ErrNotInitialized when the
// marker file is missing, which also protects against writing into an
// empty mount point when the NAS is not connected.
func OpenFolder(root string) (*Folder, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: no sync folder configured", ErrNotInitialized)
	}
	data, err := os.ReadFile(filepath.Join(root, markerName))
	if errors.Is(err, fs.ErrNotExist) {
		if _, serr := os.Stat(root); serr != nil {
			return nil, fmt.Errorf("%w: %s is not reachable (is the NAS share mounted?)", ErrNotInitialized, root)
		}
		return nil, fmt.Errorf("%w in %s (initialize it first)", ErrNotInitialized, root)
	}
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", root, err)
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("store marker %s: %w", filepath.Join(root, markerName), err)
	}
	if m.Format > FormatVersion {
		return nil, fmt.Errorf("store %s uses format %d; this WoWSync supports %d – please update", root, m.Format, FormatVersion)
	}
	return &Folder{root: root}, nil
}

// InitFolder creates a store in root (or opens it if one exists). The
// parent of root must already exist so a missing mount is not mistaken for
// an empty folder.
func InitFolder(root string) (*Folder, error) {
	if f, err := OpenFolder(root); err == nil {
		return f, nil
	}
	parent := filepath.Dir(filepath.Clean(root))
	if _, err := os.Stat(parent); err != nil {
		return nil, fmt.Errorf("cannot create store: %s is not reachable: %w", parent, err)
	}
	for _, d := range []string{root, filepath.Join(root, snapshotsDir), filepath.Join(root, objectsDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	data, _ := json.MarshalIndent(marker{Format: FormatVersion, Created: time.Now().UTC(), App: "WoWSync"}, "", "  ")
	if err := writeFileAtomic(filepath.Join(root, markerName), data); err != nil {
		return nil, err
	}
	return &Folder{root: root}, nil
}

// Root returns the folder path.
func (f *Folder) Root() string { return f.root }

// Describe implements Store.
func (f *Folder) Describe() string { return f.root }

// List implements Store.
func (f *Folder) List(ctx context.Context) ([]Info, error) {
	entries, err := os.ReadDir(filepath.Join(f.root, snapshotsDir))
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	var out []Info
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, snapExt) || strings.HasPrefix(name, ".") {
			continue
		}
		info, err := ParseID(strings.TrimSuffix(name, snapExt))
		if err != nil {
			continue // foreign file, e.g. a cloud "conflicted copy"
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, ctx.Err()
}

// Latest implements Store.
func (f *Folder) Latest(ctx context.Context) (*Snapshot, error) {
	list, err := f.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return f.Get(ctx, list[len(list)-1].ID)
}

func (f *Folder) snapPath(id string) string {
	return filepath.Join(f.root, snapshotsDir, id+snapExt)
}

// Get implements Store.
func (f *Folder) Get(ctx context.Context, id string) (*Snapshot, error) {
	if _, err := ParseID(id); err != nil {
		return nil, err
	}
	file, err := os.Open(f.snapPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("snapshot %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", id, err)
	}
	var s Snapshot
	if err := json.NewDecoder(zr).Decode(&s); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", id, err)
	}
	if s.Format > FormatVersion {
		return nil, fmt.Errorf("snapshot %s uses format %d; please update WoWSync", id, s.Format)
	}
	return &s, ctx.Err()
}

// Put implements Store.
func (f *Folder) Put(ctx context.Context, s *Snapshot) error {
	if _, err := ParseID(s.ID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dst := f.snapPath(s.ID)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("snapshot %s already exists", s.ID)
	}
	return writeAtomic(dst, func(w io.Writer) error {
		zw := gzip.NewWriter(w)
		enc := json.NewEncoder(zw)
		if err := enc.Encode(s); err != nil {
			return err
		}
		return zw.Close()
	})
}

func (f *Folder) objPath(h string) (string, error) {
	if len(h) != 64 {
		return "", fmt.Errorf("invalid object hash %q", h)
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", fmt.Errorf("invalid object hash %q", h)
	}
	return filepath.Join(f.root, objectsDir, h[:2], h), nil
}

// HasObject implements Store.
func (f *Folder) HasObject(_ context.Context, h string) (bool, error) {
	p, err := f.objPath(h)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// PutObject implements Store.
func (f *Folder) PutObject(ctx context.Context, h string, r io.Reader) error {
	p, err := f.objPath(h)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return writeAtomic(p, func(w io.Writer) error {
		zw := gzip.NewWriter(w)
		hw := sha256.New()
		if _, err := io.Copy(io.MultiWriter(zw, hw), r); err != nil {
			return err
		}
		if got := hex.EncodeToString(hw.Sum(nil)); got != h {
			return fmt.Errorf("object content hash %s does not match %s (file changed while reading?)", got, h)
		}
		return zw.Close()
	})
}

// GetObject implements Store.
func (f *Folder) GetObject(_ context.Context, h string) (io.ReadCloser, error) {
	p, err := f.objPath(h)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("object %s: %w", h, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("object %s: %w", h, err)
	}
	return &verifyReader{r: zr, f: file, h: sha256.New(), want: h}, nil
}

type verifyReader struct {
	r    io.Reader
	f    *os.File
	h    hash.Hash
	want string
}

func (v *verifyReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	if err == io.EOF {
		if got := hex.EncodeToString(v.h.Sum(nil)); got != v.want {
			return n, fmt.Errorf("object %s is corrupt (content hash %s)", v.want, got)
		}
	}
	return n, err
}

func (v *verifyReader) Close() error { return v.f.Close() }

// Prune implements Store.
func (f *Folder) Prune(ctx context.Context, keep int, minAge time.Duration) (PruneResult, error) {
	var res PruneResult
	if keep < 1 {
		keep = 1
	}
	list, err := f.List(ctx)
	if err != nil {
		return res, err
	}
	if len(list) > keep {
		for _, info := range list[:len(list)-keep] {
			if err := os.Remove(f.snapPath(info.ID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return res, err
			}
			res.SnapshotsRemoved++
		}
		list = list[len(list)-keep:]
	}
	used := map[string]bool{}
	for _, info := range list {
		s, err := f.Get(ctx, info.ID)
		if err != nil {
			return res, fmt.Errorf("prune aborted, cannot read %s: %w", info.ID, err)
		}
		for _, fe := range s.Files {
			used[fe.Hash] = true
		}
	}
	cutoff := time.Now().Add(-minAge)
	// Stale temp files from interrupted pushes.
	if entries, err := os.ReadDir(filepath.Join(f.root, snapshotsDir)); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && strings.HasPrefix(e.Name(), tmpPrefix) && fi.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(f.root, snapshotsDir, e.Name()))
			}
		}
	}
	err = filepath.WalkDir(filepath.Join(f.root, objectsDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if used[name] {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.ModTime().After(cutoff) {
			return nil // too new: a push may be in progress on another machine
		}
		if err := os.Remove(p); err == nil {
			res.ObjectsRemoved++
			res.BytesFreed += fi.Size()
		}
		return nil
	})
	return res, err
}

// writeAtomic writes via a temp file in the same folder and renames it into
// place, so readers on other machines never see half-written files.
func writeAtomic(dst string, fill func(io.Writer) error) error {
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(filepath.Dir(dst), tmpPrefix+hex.EncodeToString(rnd[:]))
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := fill(file); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		if _, serr := os.Stat(dst); serr == nil && strings.Contains(dst, string(filepath.Separator)+objectsDir+string(filepath.Separator)) {
			return nil // another machine stored the same object first
		}
		return err
	}
	return nil
}

func writeFileAtomic(dst string, data []byte) error {
	return writeAtomic(dst, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}
