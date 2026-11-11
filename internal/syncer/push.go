package syncer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/wow"
)

// PushPlan is the preview of a push.
type PushPlan struct {
	Scan   *Scan
	Latest *store.Snapshot // newest snapshot in the store, nil if empty
	Base   string          // snapshot this machine last synced
	// Conflict is set when the store has a snapshot this machine has not
	// pulled. Pushing anyway replaces those changes (they stay in history).
	Conflict   bool
	Diff       Diff // what the push changes in the store
	WoWRunning bool
	Warnings   []string

	next *store.Snapshot
}

// PushOptions control Push.
type PushOptions struct {
	// Force pushes even if the store has changes this machine has not pulled.
	Force bool
}

// PushResult describes a finished push.
type PushResult struct {
	Snapshot      *store.Snapshot // nil when nothing was pushed
	Uploaded      int
	UploadedBytes int64
	NoChanges     bool
}

// PlanPush scans local files and computes what a push would change.
func (e *Engine) PlanPush(ctx context.Context) (*PushPlan, error) {
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
	p := &PushPlan{
		Scan:     scan,
		Latest:   latest,
		Base:     state.LastSnapshot,
		Conflict: latest != nil && latest.ID != state.LastSnapshot,
		next:     e.buildSnapshot(scan, latest),
	}
	p.Diff.Files = diffFiles(scan.scopedEntries(latest, true), scan.localEntries())
	for _, fl := range scan.Flavors {
		if _, ok := scan.cvarScope(fl.Key); !ok {
			continue
		}
		var from map[string]string
		if latest != nil {
			from = latest.CVars[fl.Key].Values
		}
		if ch := diffCVarValues(from, scan.CVars[fl.Key].Values); len(ch) > 0 {
			p.Diff.CVars = append(p.Diff.CVars, CVarDiff{Flavor: fl.Key, Changes: ch})
		}
	}
	p.WoWRunning = e.wowRunning()
	p.Warnings = append(p.Warnings, scan.Warnings...)
	if p.WoWRunning {
		p.Warnings = append(p.Warnings, "WoW is running – addon settings are only saved when you log out, exit or /reload.")
	}
	if p.Conflict && !p.Diff.Empty() {
		p.Warnings = append(p.Warnings, conflictText(latest))
	}
	return p, nil
}

func conflictText(latest *store.Snapshot) string {
	return fmt.Sprintf("The store has a newer snapshot from %s (%s) that this computer has not pulled.",
		latest.Machine, latest.Created.Local().Format("2006-01-02 15:04"))
}

// buildSnapshot merges the local state into the latest snapshot: entries
// this machine manages are replaced, everything else (other flavors,
// disabled categories, excluded files) is kept.
func (e *Engine) buildSnapshot(scan *Scan, latest *store.Snapshot) *store.Snapshot {
	s := e.newSnapshot(latest)
	files := entryMap{}
	if latest != nil {
		for _, f := range latest.Files {
			if scan.InScope(f.Path) {
				continue
			}
			if k := wow.Fold(f.Path); files[k].Path == "" {
				files[k] = f
			}
		}
		for k, v := range latest.CVars {
			s.CVars[k] = v
		}
	}
	for k, f := range scan.Files {
		files[k] = f.FileEntry
	}
	for key, set := range scan.CVars {
		if _, ok := scan.cvarScope(key); ok {
			s.CVars[key] = set
		}
	}
	s.Files = make([]store.FileEntry, 0, len(files))
	for _, f := range files {
		s.Files = append(s.Files, f)
	}
	sort.Slice(s.Files, func(i, j int) bool { return strings.ToLower(s.Files[i].Path) < strings.ToLower(s.Files[j].Path) })
	return s
}

// Push uploads the planned snapshot.
func (e *Engine) Push(ctx context.Context, plan *PushPlan, opt PushOptions) (*PushResult, error) {
	if plan.Latest != nil && plan.Diff.Empty() {
		if plan.Base != plan.Latest.ID {
			if err := e.saveState(State{LastSnapshot: plan.Latest.ID, LastAction: "push", LastSync: e.now()}); err != nil {
				return nil, err
			}
		}
		return &PushResult{NoChanges: true}, nil
	}
	if plan.Conflict && !opt.Force {
		return nil, fmt.Errorf("%w: %s Pull first, or push with force to replace them", ErrConflict, conflictText(plan.Latest))
	}
	st, err := e.OpenStore()
	if err != nil {
		return nil, err
	}
	cur, err := st.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if parentID(cur) != parentID(plan.Latest) {
		return nil, ErrStoreChanged
	}

	res := &PushResult{}
	if err := e.uploadObjects(ctx, st, plan.Scan, res); err != nil {
		return nil, err
	}

	t := e.now().UTC()
	if cur != nil {
		if info, err := store.ParseID(cur.ID); err == nil && !t.After(info.Created) {
			t = info.Created.Add(time.Millisecond) // keep ids ordered despite clock skew
		}
	}
	snap := *plan.next
	snap.Created = t
	snap.ID = store.NewID(t, e.Cfg.MachineName)
	if err := st.Put(ctx, &snap); err != nil {
		return nil, fmt.Errorf("write snapshot: %w", err)
	}
	if err := e.saveState(State{LastSnapshot: snap.ID, LastAction: "push", LastSync: e.now()}); err != nil {
		return nil, err
	}
	res.Snapshot = &snap
	e.logf("Pushed snapshot %s (%d files uploaded, %s)", snap.ID, res.Uploaded, humanBytes(res.UploadedBytes))
	return res, nil
}

func (e *Engine) uploadObjects(ctx context.Context, st store.Store, scan *Scan, res *PushResult) error {
	files := make([]*LocalFile, 0, len(scan.Files))
	seen := map[string]bool{}
	for _, f := range scan.Files {
		if !seen[f.Hash] {
			seen[f.Hash] = true
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		done     atomic.Int64
		uploaded atomic.Int64
		bytes    atomic.Int64
	)
	work := make(chan *LocalFile)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		mu.Unlock()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				has, err := st.HasObject(ctx, f.Hash)
				if err != nil {
					fail(err)
					continue
				}
				if !has {
					r, err := openNormalized(f.Abs, f.Rel)
					if err != nil {
						fail(err)
						continue
					}
					err = st.PutObject(ctx, f.Hash, r)
					r.Close()
					if err != nil {
						fail(fmt.Errorf("upload %s: %w", f.Path, err))
						continue
					}
					uploaded.Add(1)
					bytes.Add(f.Size)
				}
				if n := done.Add(1); n%500 == 0 {
					e.logf("Checked %d/%d files…", n, len(files))
				}
			}
		}()
	}
feed:
	for _, f := range files {
		select {
		case work <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	res.Uploaded = int(uploaded.Load())
	res.UploadedBytes = bytes.Load()
	return nil
}
