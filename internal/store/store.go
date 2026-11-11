// Package store defines where snapshots live. A snapshot is an immutable
// manifest (path -> content hash) plus content-addressed objects, so files
// are written once and never modified. That makes the store safe on NAS
// shares and cloud folders, and identical files are stored only once.
//
// Folder is the first implementation (local/NAS/cloud-synced folder); other
// backends (S3, WebDAV, git …) can implement Store later.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// FormatVersion is the on-disk format of snapshots and the store marker.
const FormatVersion = 1

// ErrNotInitialized means the location has no WoWSync store (yet), for
// example because the NAS share is not mounted.
var ErrNotInitialized = errors.New("no WoWSync store found")

// ErrNotFound is returned for missing snapshots or objects.
var ErrNotFound = errors.New("not found")

// FileEntry is one synced file. Path is "<flavor key>/<path in flavor>",
// e.g. "retail/WTF/Account/ABC/SavedVariables/WeakAuras.lua".
type FileEntry struct {
	Path string `json:"path"`
	Hash string `json:"hash"` // sha256 of the normalized content, hex
	Size int64  `json:"size"` // size of the normalized content
}

// CVarSet holds the shared CVars of one flavor's Config.wtf and the
// patterns the pushing machine kept local (so the pulling machine leaves
// those alone too).
type CVarSet struct {
	LocalPatterns []string          `json:"local_patterns"`
	Values        map[string]string `json:"values"`
}

// Snapshot is the full synced state at one point in time.
type Snapshot struct {
	Format     int                `json:"format"`
	ID         string             `json:"id"`
	Parent     string             `json:"parent,omitempty"`
	Created    time.Time          `json:"created"`
	Machine    string             `json:"machine"`
	OS         string             `json:"os"`
	AppVersion string             `json:"app_version"`
	Files      []FileEntry        `json:"files"`
	CVars      map[string]CVarSet `json:"cvars,omitempty"` // by flavor key
}

// Info is the cheap summary of a snapshot, derived from its id.
type Info struct {
	ID      string
	Created time.Time
	Machine string
}

// PruneResult reports what Prune removed.
type PruneResult struct {
	SnapshotsRemoved int
	ObjectsRemoved   int
	BytesFreed       int64
}

// Store is a snapshot repository.
type Store interface {
	// Describe returns a human readable location.
	Describe() string
	// List returns all snapshots, oldest first.
	List(ctx context.Context) ([]Info, error)
	// Latest returns the newest snapshot, or nil if the store is empty.
	Latest(ctx context.Context) (*Snapshot, error)
	// Get loads one snapshot.
	Get(ctx context.Context, id string) (*Snapshot, error)
	// Put writes a new snapshot; ids are never overwritten.
	Put(ctx context.Context, s *Snapshot) error
	// HasObject reports whether content with this hash is stored.
	HasObject(ctx context.Context, hash string) (bool, error)
	// PutObject stores content; it fails if r does not hash to hash.
	PutObject(ctx context.Context, hash string, r io.Reader) error
	// GetObject opens stored content. Reading returns an error at EOF if
	// the content does not match its hash.
	GetObject(ctx context.Context, hash string) (io.ReadCloser, error)
	// Prune keeps the newest keep snapshots and deletes objects that no
	// snapshot references and that are older than minAge.
	Prune(ctx context.Context, keep int, minAge time.Duration) (PruneResult, error)
}

const idTimeLayout = "20060102T150405.000Z"

// NewID creates a sortable snapshot id: UTC time, machine, random suffix.
func NewID(t time.Time, machine string) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return t.UTC().Format(idTimeLayout) + "_" + SanitizeMachine(machine) + "_" + hex.EncodeToString(b[:])
}

// ParseID extracts the time and machine from a snapshot id.
func ParseID(id string) (Info, error) {
	parts := strings.Split(id, "_")
	if len(parts) != 3 {
		return Info{}, fmt.Errorf("invalid snapshot id %q", id)
	}
	t, err := time.Parse(idTimeLayout, parts[0])
	if err != nil {
		return Info{}, fmt.Errorf("invalid snapshot id %q: %w", id, err)
	}
	return Info{ID: id, Created: t, Machine: parts[1]}, nil
}

// SanitizeMachine makes a machine name safe for ids and file names.
func SanitizeMachine(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 32 {
		s = s[:32]
	}
	if s == "" {
		s = "machine"
	}
	return s
}
