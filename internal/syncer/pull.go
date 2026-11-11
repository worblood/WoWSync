package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/wow"
)

// PullPlan is the preview of a pull. Nothing is written until Pull.
type PullPlan struct {
	Scan     *Scan
	Snapshot *store.Snapshot // the snapshot that would be applied
	Diff     Diff            // what the pull changes locally
	// LocalChanges is set when local files changed since the last sync;
	// a pull overwrites them (they are in the backup).
	LocalChanges bool
	Running      []string // running WoW clients
	Warnings     []string

	base   string
	remote entryMap
	merged map[string][]byte // flavor key -> new Config.wtf
}

// PullOptions control Pull.
type PullOptions struct {
	// Force allows pulling while WoW is running (not recommended: WoW
	// overwrites SavedVariables when it exits).
	Force bool
}

// PullResult describes a finished pull.
type PullResult struct {
	Snapshot *store.Snapshot
	Backup   *BackupInfo
	Written  int
	Removed  int
	CVars    int
}

// PlanPull compares the local files with the newest snapshot.
func (e *Engine) PlanPull(ctx context.Context) (*PullPlan, error) {
	st, err := e.OpenStore()
	if err != nil {
		return nil, err
	}
	scan, err := e.Scan(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := st.Latest(ctx)
	if err != nil {
		return nil, err
	}
	state := e.LoadState()
	p := &PullPlan{Scan: scan, Snapshot: latest, base: state.LastSnapshot, merged: map[string][]byte{}}
	p.Running, _ = e.RunningClients()
	if latest == nil {
		p.Warnings = append(p.Warnings, scan.Warnings...)
		return p, nil
	}
	p.remote = scan.scopedEntries(latest, true)
	p.Diff.Files = diffFiles(scan.localEntries(), p.remote)
	for _, fl := range scan.Flavors {
		set, ok := latest.CVars[fl.Key]
		if !ok {
			continue
		}
		lc, ok := scan.cvarScope(fl.Key)
		if !ok {
			if scan.configOn {
				scan.warnf("%s: Config.wtf not found – start WoW once, then pull again to merge CVars", fl.Dir)
			}
			continue
		}
		merged, ch := wow.MergeCVars(lc.lines, set.Values, scan.protector(set))
		if len(ch) > 0 {
			p.Diff.CVars = append(p.Diff.CVars, CVarDiff{Flavor: fl.Key, Changes: ch})
			p.merged[fl.Key] = wow.FormatConfigWTF(merged, lc.crlf)
		}
	}
	if state.LastSnapshot != "" && !p.Diff.Empty() {
		if base, err := st.Get(ctx, state.LastSnapshot); err == nil {
			p.LocalChanges = scan.localDiffersFrom(base)
		}
	}
	p.Warnings = append(p.Warnings, scan.Warnings...)
	if p.LocalChanges {
		p.Warnings = append(p.Warnings, "Files on this computer changed since the last sync. Pulling overwrites those changes (they are kept in the backup).")
	}
	if len(p.Running) > 0 {
		p.Warnings = append(p.Warnings, ErrWoWRunning.Error()+".")
	}
	return p, nil
}

// Pull backs up and then applies the plan.
func (e *Engine) Pull(ctx context.Context, plan *PullPlan, opt PullOptions) (*PullResult, error) {
	if plan.Snapshot == nil {
		return nil, ErrEmptyStore
	}
	if running, _ := e.RunningClients(); len(running) > 0 && !opt.Force {
		return nil, fmt.Errorf("%w (%s)", ErrWoWRunning, strings.Join(running, ", "))
	}
	res := &PullResult{Snapshot: plan.Snapshot}
	inSync := State{LastSnapshot: plan.Snapshot.ID, LastAction: "pull", LastSync: e.now()}
	if plan.Diff.Empty() {
		return res, e.saveState(inSync)
	}
	st, err := e.OpenStore()
	if err != nil {
		return nil, err
	}
	cur, err := st.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if parentID(cur) != plan.Snapshot.ID {
		return nil, ErrStoreChanged
	}

	scan := plan.Scan
	var extra, added []string
	for _, c := range plan.Diff.Files {
		switch c.Kind {
		case wow.Added:
			added = append(added, c.Path)
			if has, err := st.HasObject(ctx, plan.remote[wow.Fold(c.Path)].Hash); err != nil || !has {
				return nil, fmt.Errorf("snapshot is incomplete, content missing for %s (%v)", c.Path, err)
			}
		case wow.Modified:
			extra = append(extra, c.Path)
			if has, err := st.HasObject(ctx, plan.remote[wow.Fold(c.Path)].Hash); err != nil || !has {
				return nil, fmt.Errorf("snapshot is incomplete, content missing for %s (%v)", c.Path, err)
			}
		case wow.Removed:
			extra = append(extra, c.Path)
		}
	}
	bk, err := e.createBackup(ctx, "pre-pull", scan.Flavors, extra, added, plan.Snapshot.ID, plan.base)
	if err != nil {
		return nil, fmt.Errorf("backup failed, nothing was changed: %w", err)
	}
	res.Backup = bk
	e.logf("Backup %s created", bk.ID)

	cache := e.loadHashCache()
	defer cache.save()
	failed := func(err error) error {
		return fmt.Errorf("pull stopped after %d changes: %w – restore backup %s to undo", res.Written+res.Removed, err, bk.ID)
	}
	for _, c := range plan.Diff.Files {
		key, rel, _ := SplitSnapshotPath(c.Path)
		fl := scan.present[key]
		local := scan.Files[wow.Fold(c.Path)]
		switch c.Kind {
		case wow.Added, wow.Modified:
			abs := filepath.Join(fl.Path, filepath.FromSlash(rel))
			if local != nil {
				abs = local.Abs // keep the local spelling of the name
			}
			entry := plan.remote[wow.Fold(c.Path)]
			if err := writeObject(ctx, st, entry.Hash, abs); err != nil {
				return nil, failed(err)
			}
			if info, err := os.Stat(abs); err == nil {
				cache.put(abs, info, entry.Hash, entry.Size)
			}
			res.Written++
		case wow.Removed:
			if local == nil {
				continue
			}
			if err := os.Remove(local.Abs); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, failed(err)
			}
			removeEmptyParents(local.Abs, scanRootDir(fl, rel))
			res.Removed++
		}
	}
	for key, data := range plan.merged {
		lc := scan.configs[key]
		if err := replaceFile(lc.abs, data); err != nil {
			return nil, failed(err)
		}
	}
	res.CVars = plan.Diff.CVarCount()
	if err := e.saveState(inSync); err != nil {
		return nil, err
	}
	if err := e.PruneBackups(e.Cfg.BackupKeep); err != nil {
		e.logf("warning: pruning old backups failed: %v", err)
	}
	e.logf("Pulled snapshot %s: %d written, %d removed, %d CVars", plan.Snapshot.ID, res.Written, res.Removed, res.CVars)
	return res, nil
}

// scanRootDir returns the scan root ("WTF" or "Interface/AddOns") that
// contains rel; empty parent folders are removed up to it.
func scanRootDir(fl wow.Flavor, rel string) string {
	for _, r := range wow.ScanRoots {
		if strings.HasPrefix(strings.ToLower(rel), strings.ToLower(r)+"/") {
			return filepath.Join(fl.Path, filepath.FromSlash(r))
		}
	}
	return fl.Path
}

func removeEmptyParents(abs, stop string) {
	stop = filepath.Clean(stop)
	for dir := filepath.Dir(abs); len(dir) > len(stop) && strings.HasPrefix(dir, stop); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}

// writeObject copies stored content to abs atomically.
func writeObject(ctx context.Context, st store.Store, hash, abs string) error {
	rc, err := st.GetObject(ctx, hash)
	if err != nil {
		return err
	}
	defer rc.Close()
	return replaceWith(abs, func(w io.Writer) error {
		_, err := io.Copy(w, rc)
		return err
	})
}

func replaceFile(abs string, data []byte) error {
	return replaceWith(abs, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// replaceWith writes a temp file next to abs and renames it over abs. A
// read-only target (some players protect SavedVariables) is made writable.
func replaceWith(abs string, fill func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".wowsync-*")
	if err != nil {
		return err
	}
	if err := fill(tmp); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", abs, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		if os.Chmod(abs, 0o644) == nil {
			err = os.Rename(tmp.Name(), abs)
		}
		if err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	return nil
}
