package syncer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/wow"
)

// Change is one file that an operation adds, modifies or removes.
type Change struct {
	Path     string // snapshot path
	Kind     wow.ChangeKind
	Category string
	OldSize  int64
	NewSize  int64
}

// CVarDiff lists Config.wtf changes for one flavor.
type CVarDiff struct {
	Flavor  string
	Changes []wow.CVarChange
}

// Diff is what an operation would change.
type Diff struct {
	Files []Change
	CVars []CVarDiff
}

// Empty reports whether nothing would change.
func (d Diff) Empty() bool { return len(d.Files) == 0 && d.CVarCount() == 0 }

// Count returns the number of file changes of one kind.
func (d Diff) Count(k wow.ChangeKind) int {
	n := 0
	for _, c := range d.Files {
		if c.Kind == k {
			n++
		}
	}
	return n
}

// CVarCount returns the number of changed CVars.
func (d Diff) CVarCount() int {
	n := 0
	for _, c := range d.CVars {
		n += len(c.Changes)
	}
	return n
}

// Summary is a one-line description, e.g. "2 added, 1 modified, 0 removed, 3 CVars".
func (d Diff) Summary() string {
	if d.Empty() {
		return "no changes"
	}
	s := fmt.Sprintf("%d added, %d modified, %d removed", d.Count(wow.Added), d.Count(wow.Modified), d.Count(wow.Removed))
	if n := d.CVarCount(); n > 0 {
		s += fmt.Sprintf(", %d CVar(s) in Config.wtf", n)
	}
	return s
}

// ByCategory groups file changes by category id, in category order.
func (d Diff) ByCategory() []CategoryChanges {
	groups := map[string][]Change{}
	for _, c := range d.Files {
		groups[c.Category] = append(groups[c.Category], c)
	}
	var out []CategoryChanges
	for _, cat := range wow.Categories {
		if g := groups[cat.ID]; len(g) > 0 {
			out = append(out, CategoryChanges{Category: cat, Changes: g})
		}
	}
	return out
}

// CategoryChanges is a group of changes in one category.
type CategoryChanges struct {
	Category wow.Category
	Changes  []Change
}

// entryMap indexes file entries by folded path.
type entryMap map[string]store.FileEntry

// diffFiles returns the changes that turn "from" into "to".
func diffFiles(from, to entryMap) []Change {
	var out []Change
	for k, t := range to {
		f, ok := from[k]
		switch {
		case !ok:
			out = append(out, Change{Path: t.Path, Kind: wow.Added, NewSize: t.Size})
		case f.Hash != t.Hash:
			out = append(out, Change{Path: t.Path, Kind: wow.Modified, OldSize: f.Size, NewSize: t.Size})
		}
	}
	for k, f := range from {
		if _, ok := to[k]; !ok {
			out = append(out, Change{Path: f.Path, Kind: wow.Removed, OldSize: f.Size})
		}
	}
	for i := range out {
		if _, rel, ok := SplitSnapshotPath(out[i].Path); ok {
			out[i].Category = wow.Classify(rel)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
	return out
}

// diffCVarValues compares two CVar sets (for push previews).
func diffCVarValues(from, to map[string]string) []wow.CVarChange {
	fl := map[string]string{}
	for k := range from {
		fl[strings.ToLower(k)] = k
	}
	tl := map[string]string{}
	for k := range to {
		tl[strings.ToLower(k)] = k
	}
	var out []wow.CVarChange
	for lk, tk := range tl {
		fk, ok := fl[lk]
		switch {
		case !ok:
			out = append(out, wow.CVarChange{Name: tk, New: to[tk], Kind: wow.Added})
		case from[fk] != to[tk]:
			out = append(out, wow.CVarChange{Name: tk, Old: from[fk], New: to[tk], Kind: wow.Modified})
		}
	}
	for lk, fk := range fl {
		if _, ok := tl[lk]; !ok {
			out = append(out, wow.CVarChange{Name: fk, Old: from[fk], Kind: wow.Removed})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// localEntries returns the scanned files as an entryMap.
func (s *Scan) localEntries() entryMap {
	m := entryMap{}
	for k, f := range s.Files {
		m[k] = f.FileEntry
	}
	return m
}

// scopedEntries returns the snapshot's files that this machine manages.
// Invalid paths are reported as warnings and ignored.
func (s *Scan) scopedEntries(snap *store.Snapshot, warn bool) entryMap {
	m := entryMap{}
	if snap == nil {
		return m
	}
	for _, f := range snap.Files {
		if err := wow.ValidateRel(f.Path); err != nil {
			if warn {
				s.warnf("ignoring unsafe path in snapshot: %v", err)
			}
			continue
		}
		if !s.InScope(f.Path) {
			continue
		}
		k := wow.Fold(f.Path)
		if _, dup := m[k]; dup {
			if warn {
				s.warnf("%s: duplicate path (letter case) in snapshot – ignored", f.Path)
			}
			continue
		}
		m[k] = f
	}
	return m
}

// localDiffersFrom reports whether the managed local state differs from snap.
func (s *Scan) localDiffersFrom(snap *store.Snapshot) bool {
	if len(diffFiles(s.scopedEntries(snap, false), s.localEntries())) > 0 {
		return true
	}
	for key := range s.present {
		if _, ok := s.cvarScope(key); !ok {
			continue
		}
		remote, ok := snap.CVars[key]
		if !ok {
			continue
		}
		local := s.CVars[key]
		if len(filterCVarChanges(diffCVarValues(remote.Values, local.Values), s.protector(remote))) > 0 {
			return true
		}
	}
	return false
}

// protector returns the CVar names left alone when merging remote: those
// kept local on this machine or on the machine that pushed remote.
func (s *Scan) protector(remote store.CVarSet) func(string) bool {
	theirs := wow.CVarMatcher(remote.LocalPatterns)
	return func(name string) bool { return s.localCVars.Match(name) || theirs.Match(name) }
}

func filterCVarChanges(ch []wow.CVarChange, protected func(string) bool) []wow.CVarChange {
	var out []wow.CVarChange
	for _, c := range ch {
		if !protected(c.Name) {
			out = append(out, c)
		}
	}
	return out
}
