package syncer

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/worblood/WoWSync/internal/config"
	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/wow"
)

// LocalFile is a managed file found on disk.
type LocalFile struct {
	store.FileEntry        // Path is the snapshot path ("retail/WTF/…")
	Abs             string // absolute path on disk
	Flavor          string // flavor key
	Rel             string // path inside the flavor folder
}

// localConfig is the parsed Config.wtf of one flavor.
type localConfig struct {
	abs   string
	lines []wow.ConfigLine
	crlf  bool
}

// Scan is the managed local state of all configured flavors.
type Scan struct {
	Flavors  []wow.Flavor          // flavors whose folder exists
	Files    map[string]*LocalFile // by folded snapshot path
	CVars    map[string]store.CVarSet
	Warnings []string

	configs     map[string]*localConfig // by flavor key
	rules       *wow.Rules
	present     map[string]wow.Flavor // by flavor key
	skipped     map[string]bool       // folded snapshot paths left alone (too large)
	skippedDirs []string              // folded snapshot path prefixes left alone (symlinks)
	localCVars  wow.CVarMatcher
	configOn    bool
}

// SnapshotPath joins a flavor key and a path inside the flavor folder.
func SnapshotPath(flavorKey, rel string) string { return flavorKey + "/" + rel }

// SplitSnapshotPath splits "retail/WTF/x" into "retail", "WTF/x".
func SplitSnapshotPath(p string) (flavorKey, rel string, ok bool) {
	i := strings.IndexByte(p, '/')
	if i <= 0 || i == len(p)-1 {
		return "", "", false
	}
	return p[:i], p[i+1:], true
}

// InScope reports whether this machine manages the snapshot path p: its
// flavor is present, its category is enabled, it is not excluded and it
// was not skipped (too large, symlink). Files out of scope are preserved
// on push and ignored on pull.
func (s *Scan) InScope(p string) bool {
	key, rel, ok := SplitSnapshotPath(p)
	if !ok {
		return false
	}
	if _, ok := s.present[key]; !ok {
		return false
	}
	if !s.rules.Managed(rel) {
		return false
	}
	f := wow.Fold(p)
	if s.skipped[f] {
		return false
	}
	for _, d := range s.skippedDirs {
		if strings.HasPrefix(f, d+"/") {
			return false
		}
	}
	return true
}

// cvarScope reports whether Config.wtf of this flavor is merged here.
func (s *Scan) cvarScope(key string) (*localConfig, bool) {
	if !s.configOn {
		return nil, false
	}
	if _, ok := s.present[key]; !ok {
		return nil, false
	}
	lc, ok := s.configs[key]
	return lc, ok
}

func (s *Scan) warnf(format string, args ...any) {
	s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...))
}

type candidate struct {
	abs, snap, rel, key string
	info                fs.FileInfo
}

// Scan reads the managed local files and hashes them (with a cache keyed
// on size and modification time).
func (e *Engine) Scan(ctx context.Context) (*Scan, error) {
	cfg := e.Cfg
	s := &Scan{
		Files:      map[string]*LocalFile{},
		CVars:      map[string]store.CVarSet{},
		configs:    map[string]*localConfig{},
		rules:      cfg.Rules(),
		present:    map[string]wow.Flavor{},
		skipped:    map[string]bool{},
		localCVars: wow.CVarMatcher(cfg.EffectiveLocalCVars()),
		configOn:   cfg.Categories["config"],
	}
	warnBytes := int64(cfg.WarnFileSizeMB) << 20
	maxBytes := int64(cfg.MaxFileSizeMB) << 20

	var cands []candidate
	for _, fl := range cfg.FlavorList() {
		st, err := os.Stat(fl.Path)
		if err != nil || !st.IsDir() {
			s.warnf("%s: folder not found (%s) – skipped", fl.Dir, fl.Path)
			continue
		}
		s.Flavors = append(s.Flavors, fl)
		s.present[fl.Key] = fl

		if s.configOn {
			abs := filepath.Join(fl.Path, filepath.FromSlash(wow.ConfigWTF))
			if data, err := os.ReadFile(abs); err == nil {
				lines, crlf := wow.ParseConfigWTF(data)
				s.configs[fl.Key] = &localConfig{abs: abs, lines: lines, crlf: crlf}
				s.CVars[fl.Key] = store.CVarSet{
					LocalPatterns: cfg.EffectiveLocalCVars(),
					Values:        wow.SyncedCVars(lines, s.localCVars),
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("read %s: %w", abs, err)
			}
		}

		for _, root := range wow.ScanRoots {
			base := filepath.Join(fl.Path, filepath.FromSlash(root))
			err := filepath.WalkDir(base, func(abs string, d fs.DirEntry, err error) error {
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) && abs == base {
						return nil
					}
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				r, err := filepath.Rel(fl.Path, abs)
				if err != nil {
					return err
				}
				rel := wow.NormalizeRel(r)
				snap := SnapshotPath(fl.Key, rel)
				if d.Type()&fs.ModeSymlink != 0 {
					s.skippedDirs = append(s.skippedDirs, wow.Fold(snap))
					s.skipped[wow.Fold(snap)] = true
					s.warnf("%s: symbolic link not synced", snap)
					return nil
				}
				if d.IsDir() {
					if abs != base && s.rules.ExcludedDir(rel) {
						return filepath.SkipDir
					}
					return nil
				}
				if !d.Type().IsRegular() || !s.rules.Managed(rel) {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				if maxBytes > 0 && info.Size() > maxBytes {
					s.skipped[wow.Fold(snap)] = true
					s.warnf("%s: %s is above the limit of %d MB – not synced", snap, humanBytes(info.Size()), cfg.MaxFileSizeMB)
					return nil
				}
				if info.Size() > warnBytes {
					s.warnf("%s: large file (%s)", snap, humanBytes(info.Size()))
				}
				cands = append(cands, candidate{abs: abs, snap: snap, rel: rel, key: fl.Key, info: info})
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("scan %s: %w", base, err)
			}
		}
	}

	cache := e.loadHashCache()
	if err := hashAll(ctx, cands, cache); err != nil {
		return nil, err
	}
	for _, c := range cands {
		ce := cache.get(c.abs)
		folded := wow.Fold(c.snap)
		if prev, dup := s.Files[folded]; dup {
			s.warnf("%s and %s differ only in letter case – only the first is synced", prev.Path, c.snap)
			continue
		}
		s.Files[folded] = &LocalFile{
			FileEntry: store.FileEntry{Path: c.snap, Hash: ce.Hash, Size: ce.NSize},
			Abs:       c.abs, Flavor: c.key, Rel: c.rel,
		}
	}
	cache.save()
	return s, nil
}

func hashAll(ctx context.Context, cands []candidate, cache *hashCache) error {
	work := make(chan candidate)
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				if _, ok := cache.lookup(c.abs, c.info); ok {
					continue
				}
				h, n, err := hashFile(c.abs, c.rel)
				if err != nil {
					select {
					case errc <- fmt.Errorf("hash %s: %w", c.abs, err):
					default:
					}
					continue
				}
				cache.put(c.abs, c.info, h, n)
			}
		}()
	}
	var err error
loop:
	for _, c := range cands {
		select {
		case work <- c:
		case err = <-errc:
			break loop
		case <-ctx.Done():
			err = ctx.Err()
			break loop
		}
	}
	close(work)
	wg.Wait()
	if err != nil {
		return err
	}
	select {
	case err = <-errc:
		return err
	default:
		return nil
	}
}

// normalizeText converts CRLF to LF unless the content looks binary.
func normalizeText(b []byte) []byte {
	if bytes.IndexByte(b, 0) >= 0 {
		return b
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// openNormalized opens a file as it is stored: text files with LF line
// endings, everything else byte for byte.
func openNormalized(abs, rel string) (io.ReadCloser, error) {
	if wow.IsTextFile(rel) {
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(normalizeText(data))), nil
	}
	return os.Open(abs)
}

func hashFile(abs, rel string) (string, int64, error) {
	r, err := openNormalized(abs, rel)
	if err != nil {
		return "", 0, err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// hashCache remembers content hashes by path, size and mtime.
type hashCache struct {
	path  string
	mu    sync.Mutex
	m     map[string]cacheEntry
	dirty bool
}

type cacheEntry struct {
	Size  int64  `json:"s"`
	MTime int64  `json:"t"`
	Hash  string `json:"h"`
	NSize int64  `json:"n"`
}

func (e *Engine) loadHashCache() *hashCache {
	c := &hashCache{path: e.Paths.HashCache, m: map[string]cacheEntry{}}
	if c.path == "" {
		return c
	}
	f, err := os.Open(c.path)
	if err != nil {
		return c
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return c
	}
	_ = json.NewDecoder(zr).Decode(&c.m)
	if c.m == nil {
		c.m = map[string]cacheEntry{}
	}
	return c
}

func (c *hashCache) lookup(abs string, info fs.FileInfo) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ce, ok := c.m[abs]
	if ok && ce.Size == info.Size() && ce.MTime == info.ModTime().UnixNano() {
		return ce, true
	}
	return cacheEntry{}, false
}

func (c *hashCache) get(abs string) cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[abs]
}

func (c *hashCache) put(abs string, info fs.FileInfo, hash string, nsize int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[abs] = cacheEntry{Size: info.Size(), MTime: info.ModTime().UnixNano(), Hash: hash, NSize: nsize}
	c.dirty = true
}

func (c *hashCache) save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty || c.path == "" {
		return
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(c.m); err != nil {
		return
	}
	if err := zw.Close(); err != nil {
		return
	}
	if err := config.WriteFileAtomic(c.path, buf.Bytes()); err == nil {
		c.dirty = false
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
