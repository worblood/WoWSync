package syncer

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/worblood/WoWSync/internal/wow"
)

const backupManifest = "manifest.json"

// BackupInfo describes a local backup zip.
type BackupInfo struct {
	ID      string            `json:"id"`
	Created time.Time         `json:"created"`
	Reason  string            `json:"reason"`  // pre-pull, pre-restore
	Machine string            `json:"machine"` //
	Flavors map[string]string `json:"flavors"` // flavor key -> folder at backup time
	// Snapshot is the snapshot that was about to be pulled.
	Snapshot string `json:"snapshot,omitempty"`
	// PrevSnapshot is what this machine had synced before; restoring the
	// backup also restores that sync state.
	PrevSnapshot string `json:"prev_snapshot,omitempty"`
	// Added are snapshot paths that did not exist before; restore deletes them.
	Added []string `json:"added,omitempty"`
	Files int      `json:"files"`

	Path string `json:"-"`
	Size int64  `json:"-"`
}

// createBackup zips the whole WTF folder of each flavor plus the extra
// snapshot paths (files outside WTF that are about to change).
func (e *Engine) createBackup(ctx context.Context, reason string, flavors []wow.Flavor, extra, added []string, snapshot, prev string) (*BackupInfo, error) {
	if err := os.MkdirAll(e.Paths.BackupDir, 0o755); err != nil {
		return nil, err
	}
	now := e.now()
	id := now.Format("20060102-150405.000") + "_" + reason
	dst := filepath.Join(e.Paths.BackupDir, id+".zip")
	for i := 2; fileExists(dst); i++ {
		id = fmt.Sprintf("%s_%s-%d", now.Format("20060102-150405.000"), reason, i)
		dst = filepath.Join(e.Paths.BackupDir, id+".zip")
	}
	info := &BackupInfo{
		ID: id, Created: now, Reason: reason, Machine: e.Cfg.MachineName,
		Flavors: map[string]string{}, Snapshot: snapshot, PrevSnapshot: prev, Added: added,
	}
	byKey := map[string]wow.Flavor{}
	for _, fl := range flavors {
		byKey[fl.Key] = fl
		info.Flavors[fl.Key] = fl.Path
	}

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(out)
	seen := map[string]bool{}
	add := func(snapPath, abs string) error {
		k := wow.Fold(snapPath)
		if seen[k] {
			return nil
		}
		fi, err := os.Lstat(abs)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		seen[k] = true
		hdr, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		hdr.Name = "files/" + snapPath
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		info.Files++
		return err
	}
	fail := func(err error) (*BackupInfo, error) {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return nil, fmt.Errorf("backup: %w", err)
	}

	for _, fl := range flavors {
		wtf := filepath.Join(fl.Path, "WTF")
		err := filepath.WalkDir(wtf, func(abs string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && abs == wtf {
					return nil
				}
				return err
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if !d.Type().IsRegular() {
				return nil
			}
			r, err := filepath.Rel(fl.Path, abs)
			if err != nil {
				return err
			}
			return add(SnapshotPath(fl.Key, wow.NormalizeRel(r)), abs)
		})
		if err != nil {
			return fail(err)
		}
	}
	for _, p := range extra {
		key, rel, ok := SplitSnapshotPath(p)
		fl, found := byKey[key]
		if !ok || !found {
			continue
		}
		if err := add(p, filepath.Join(fl.Path, filepath.FromSlash(rel))); err != nil {
			return fail(err)
		}
	}
	mw, err := zw.Create(backupManifest)
	if err != nil {
		return fail(err)
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	info.Path = dst
	if fi, err := os.Stat(dst); err == nil {
		info.Size = fi.Size()
	}
	return info, nil
}

// ListBackups returns local backups, newest first.
func (e *Engine) ListBackups() ([]BackupInfo, error) {
	entries, err := os.ReadDir(e.Paths.BackupDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []BackupInfo
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".zip") {
			continue
		}
		p := filepath.Join(e.Paths.BackupDir, ent.Name())
		bi, err := readBackupInfo(p)
		if err != nil {
			continue
		}
		out = append(out, *bi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func readBackupInfo(p string) (*BackupInfo, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	bi, err := manifestOf(&zr.Reader)
	if err != nil {
		return nil, err
	}
	bi.Path = p
	if fi, err := os.Stat(p); err == nil {
		bi.Size = fi.Size()
	}
	return bi, nil
}

func manifestOf(zr *zip.Reader) (*BackupInfo, error) {
	for _, f := range zr.File {
		if f.Name != backupManifest {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		var bi BackupInfo
		if err := json.NewDecoder(rc).Decode(&bi); err != nil {
			return nil, err
		}
		return &bi, nil
	}
	return nil, errors.New("not a WoWSync backup (manifest missing)")
}

// FindBackup resolves an id, unique id prefix or "latest".
func (e *Engine) FindBackup(id string) (*BackupInfo, error) {
	list, err := e.ListBackups()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errors.New("there are no backups")
	}
	if id == "" || id == "latest" {
		return &list[0], nil
	}
	var match []BackupInfo
	for _, b := range list {
		if b.ID == id {
			return &b, nil
		}
		if strings.HasPrefix(b.ID, id) {
			match = append(match, b)
		}
	}
	switch len(match) {
	case 0:
		return nil, fmt.Errorf("backup %q not found", id)
	case 1:
		return &match[0], nil
	default:
		return nil, fmt.Errorf("backup %q is ambiguous (%d matches)", id, len(match))
	}
}

// RestoreOptions control Restore.
type RestoreOptions struct {
	Force bool // allow while WoW is running
}

// RestoreResult describes a finished restore.
type RestoreResult struct {
	Backup   *BackupInfo
	Safety   *BackupInfo // backup of the state just before restoring
	Restored int
	Removed  int
}

// Restore puts the files of a backup back and deletes files the pull had
// added. The current state is backed up first.
func (e *Engine) Restore(ctx context.Context, id string, opt RestoreOptions) (*RestoreResult, error) {
	bi, err := e.FindBackup(id)
	if err != nil {
		return nil, err
	}
	if running, _ := e.RunningClients(); len(running) > 0 && !opt.Force {
		return nil, fmt.Errorf("%w (%s)", ErrWoWRunning, strings.Join(running, ", "))
	}
	zr, err := zip.OpenReader(bi.Path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	// Where does each flavor live now? Prefer the current config.
	flavors := map[string]wow.Flavor{}
	for key, p := range bi.Flavors {
		flavors[key] = wow.Flavor{Dir: filepath.Base(p), Key: key, Path: p}
	}
	for _, fl := range e.Cfg.FlavorList() {
		if _, ok := flavors[fl.Key]; ok {
			flavors[fl.Key] = fl
		}
	}
	target := func(snapPath string) (string, wow.Flavor, string, error) {
		if err := wow.ValidateRel(snapPath); err != nil {
			return "", wow.Flavor{}, "", err
		}
		key, rel, ok := SplitSnapshotPath(snapPath)
		fl, found := flavors[key]
		if !ok || !found {
			return "", wow.Flavor{}, "", fmt.Errorf("unknown flavor in %q", snapPath)
		}
		return filepath.Join(fl.Path, filepath.FromSlash(rel)), fl, rel, nil
	}

	// Safety backup: everything restore will overwrite or delete.
	var extra, created []string
	var fls []wow.Flavor
	for _, fl := range flavors {
		fls = append(fls, fl)
	}
	sort.Slice(fls, func(i, j int) bool { return fls[i].Key < fls[j].Key })
	for _, f := range zr.File {
		p, ok := strings.CutPrefix(f.Name, "files/")
		if !ok {
			continue
		}
		abs, _, _, err := target(p)
		if err != nil {
			return nil, fmt.Errorf("backup %s: %w", bi.ID, err)
		}
		if fileExists(abs) {
			extra = append(extra, p)
		} else {
			created = append(created, p)
		}
	}
	extra = append(extra, bi.Added...)
	state := e.LoadState()
	safety, err := e.createBackup(ctx, "pre-restore", fls, extra, created, "", state.LastSnapshot)
	if err != nil {
		return nil, fmt.Errorf("safety backup failed, nothing was changed: %w", err)
	}
	res := &RestoreResult{Backup: bi, Safety: safety}

	for _, f := range zr.File {
		p, ok := strings.CutPrefix(f.Name, "files/")
		if !ok {
			continue
		}
		abs, _, _, _ := target(p)
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		err = replaceWith(abs, func(w io.Writer) error {
			_, err := io.Copy(w, rc)
			return err
		})
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("restore stopped after %d files: %w", res.Restored, err)
		}
		os.Chtimes(abs, f.Modified, f.Modified)
		res.Restored++
	}
	for _, p := range bi.Added {
		abs, fl, rel, err := target(p)
		if err != nil {
			continue
		}
		if err := os.Remove(abs); err == nil {
			removeEmptyParents(abs, scanRootDir(fl, rel))
			res.Removed++
		}
	}
	if err := e.saveState(State{LastSnapshot: bi.PrevSnapshot, LastAction: "restore", LastSync: e.now()}); err != nil {
		return nil, err
	}
	e.logf("Restored backup %s (%d files restored, %d removed)", bi.ID, res.Restored, res.Removed)
	return res, nil
}

// PruneBackups deletes all but the newest keep backups.
func (e *Engine) PruneBackups(keep int) error {
	if keep < 1 {
		return nil
	}
	list, err := e.ListBackups()
	if err != nil {
		return err
	}
	var errs []error
	for i := keep; i < len(list); i++ {
		if err := os.Remove(list[i].Path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
