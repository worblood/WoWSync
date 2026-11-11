package syncer

import (
	"context"
	"time"

	"github.com/worblood/WoWSync/internal/store"
)

// Advice is the suggested next action.
type Advice string

// Possible advice.
const (
	AdviceInSync     Advice = "in-sync"
	AdvicePush       Advice = "push"
	AdvicePull       Advice = "pull"
	AdviceConflict   Advice = "conflict"
	AdviceFirstSync  Advice = "first-sync"
	AdviceEmptyStore Advice = "empty-store"
)

// Status summarizes local vs. store.
type Status struct {
	Latest       *store.Info // newest snapshot, nil if the store is empty
	State        State
	Running      []string
	LocalChanged bool // local files changed since the last sync
	StoreChanged bool // the store has a snapshot this machine has not synced
	Diff         Diff // what a pull would change
	Advice       Advice
	Message      string
	Warnings     []string
	Plan         *PullPlan
}

// Status computes the current sync status.
func (e *Engine) Status(ctx context.Context) (*Status, error) {
	plan, err := e.PlanPull(ctx)
	if err != nil {
		return nil, err
	}
	s := &Status{State: e.LoadState(), Running: plan.Running, Diff: plan.Diff, Warnings: plan.Warnings, Plan: plan}
	if plan.Snapshot == nil {
		s.Advice = AdviceEmptyStore
		s.Message = "The store is empty – push to create the first snapshot."
		return s, nil
	}
	s.Latest = &store.Info{ID: plan.Snapshot.ID, Created: plan.Snapshot.Created, Machine: plan.Snapshot.Machine}
	s.StoreChanged = plan.Snapshot.ID != s.State.LastSnapshot
	if !plan.Diff.Empty() && s.State.LastSnapshot != "" {
		if !s.StoreChanged {
			s.LocalChanged = true
		} else {
			s.LocalChanged = plan.LocalChanges
		}
	}
	switch {
	case plan.Diff.Empty():
		s.Advice = AdviceInSync
		s.Message = "In sync with the newest snapshot."
	case s.State.LastSnapshot == "":
		s.Advice = AdviceFirstSync
		s.Message = "First sync on this computer: pull to get the shared settings, or push to replace them with this computer's."
	case s.StoreChanged && s.LocalChanged:
		s.Advice = AdviceConflict
		s.Message = "Both this computer and the store changed. Pull to take the store's version (your files are backed up), or push with force to keep yours."
	case s.StoreChanged:
		s.Advice = AdvicePull
		s.Message = "The store has newer settings from " + plan.Snapshot.Machine + " – pull to get them."
	default:
		s.Advice = AdvicePush
		s.Message = "This computer has changes that are not in the store – push to share them."
	}
	return s, nil
}

// WatchWoW polls for running WoW clients every interval and calls
// onChange(true) when WoW starts and onChange(false) when the last client
// exits. It returns when ctx is cancelled.
func WatchWoW(ctx context.Context, interval time.Duration, running func() ([]string, error), onChange func(running bool)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	was := false
	if c, err := running(); err == nil {
		was = len(c) > 0
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, err := running()
		if err != nil {
			continue
		}
		if now := len(c) > 0; now != was {
			was = now
			onChange(now)
		}
	}
}
