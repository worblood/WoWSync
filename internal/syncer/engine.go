// Package syncer is the sync engine: it scans the local WoW folders,
// compares them with the newest snapshot in the store, and pushes, pulls,
// backs up and restores. It has no GUI dependencies.
package syncer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"time"

	"github.com/worblood/WoWSync/internal/config"
	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/version"
	"github.com/worblood/WoWSync/internal/wow"
)

// Errors returned by Push, Pull and Restore.
var (
	// ErrWoWRunning: WoW must be closed before files are written.
	ErrWoWRunning = errors.New("World of Warcraft is running – close it first")
	// ErrConflict: the store has a snapshot this machine has not pulled.
	ErrConflict = errors.New("the store has newer changes from another computer")
	// ErrStoreChanged: the store changed between preview and apply.
	ErrStoreChanged = errors.New("the store changed since the preview – refresh and try again")
	// ErrEmptyStore: nothing to pull.
	ErrEmptyStore = errors.New("the store is empty – push first")
)

// Engine runs sync operations for one machine.
type Engine struct {
	Cfg   *config.Config
	Paths config.Paths

	// Store overrides the store opened from Cfg.StorePath (tests, other backends).
	Store store.Store
	// IsWoWRunning defaults to wow.RunningClients.
	IsWoWRunning func() ([]string, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// Logf receives progress messages; may be nil.
	Logf func(format string, args ...any)
}

// New creates an engine for the given config and paths.
func New(cfg *config.Config, paths config.Paths) *Engine {
	return &Engine{Cfg: cfg, Paths: paths}
}

func (e *Engine) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// RunningClients returns running WoW processes.
func (e *Engine) RunningClients() ([]string, error) {
	if e.IsWoWRunning != nil {
		return e.IsWoWRunning()
	}
	return wow.RunningClients()
}

func (e *Engine) wowRunning() bool {
	c, err := e.RunningClients()
	if err != nil {
		e.logf("warning: could not check for running WoW: %v", err)
		return false
	}
	return len(c) > 0
}

// OpenStore returns the configured store.
func (e *Engine) OpenStore() (store.Store, error) {
	if e.Store != nil {
		return e.Store, nil
	}
	f, err := store.OpenFolder(e.Cfg.StorePath)
	if err != nil {
		return nil, err
	}
	e.Store = f
	return f, nil
}

// InitStore creates a store at Cfg.StorePath.
func (e *Engine) InitStore() (store.Store, error) {
	f, err := store.InitFolder(e.Cfg.StorePath)
	if err != nil {
		return nil, err
	}
	e.Store = f
	return f, nil
}

// State is what this machine last synced. It is stored next to the config.
type State struct {
	StorePath    string    `json:"store_path"`
	LastSnapshot string    `json:"last_snapshot"`
	LastAction   string    `json:"last_action"` // push, pull, restore
	LastSync     time.Time `json:"last_sync"`
}

// LoadState reads the sync state. State recorded for another store path is ignored.
func (e *Engine) LoadState() State {
	var s State
	data, err := os.ReadFile(e.Paths.StateFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			e.logf("warning: cannot read state: %v", err)
		}
		return State{}
	}
	if err := json.Unmarshal(data, &s); err != nil || s.StorePath != e.Cfg.StorePath {
		return State{}
	}
	return s
}

func (e *Engine) saveState(s State) error {
	s.StorePath = e.Cfg.StorePath
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(e.Paths.StateFile, append(data, '\n')); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

func (e *Engine) newSnapshot(parent *store.Snapshot) *store.Snapshot {
	return &store.Snapshot{
		Format:     store.FormatVersion,
		Machine:    e.Cfg.MachineName,
		OS:         runtime.GOOS,
		AppVersion: version.String(),
		CVars:      map[string]store.CVarSet{},
		Parent:     parentID(parent),
	}
}

func parentID(s *store.Snapshot) string {
	if s == nil {
		return ""
	}
	return s.ID
}
