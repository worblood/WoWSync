package wow

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Flavor is one game client inside the WoW install folder, e.g. _retail_.
type Flavor struct {
	Dir  string // folder name, e.g. "_retail_"
	Key  string // name used in snapshots, e.g. "retail"
	Path string // absolute path to the folder
}

// KnownFlavors lists flavor folders in display order. Unknown folders of
// the form _name_ are still detected.
var KnownFlavors = []string{
	"_retail_", "_classic_", "_classic_era_", "_anniversary_",
	"_ptr_", "_xptr_", "_beta_", "_classic_ptr_", "_classic_beta_", "_classic_era_ptr_",
}

// IsFlavorDir reports whether name looks like a flavor folder (_xxx_).
func IsFlavorDir(name string) bool {
	return len(name) > 2 && strings.HasPrefix(name, "_") && strings.HasSuffix(name, "_") &&
		!strings.ContainsAny(name, `/\ `)
}

// FlavorKey maps a flavor folder name to its snapshot key.
func FlavorKey(dir string) string {
	return strings.Trim(strings.ToLower(dir), "_")
}

// NewFlavor builds a Flavor for dir inside root.
func NewFlavor(root, dir string) Flavor {
	return Flavor{Dir: dir, Key: FlavorKey(dir), Path: filepath.Join(root, dir)}
}

// SplitInstallPath accepts either the WoW install root or a flavor folder
// (e.g. ".../World of Warcraft/_retail_") and returns the root plus the
// flavor folder name ("" if p is the root).
func SplitInstallPath(p string) (root, flavorDir string) {
	p = filepath.Clean(p)
	base := filepath.Base(p)
	if IsFlavorDir(base) {
		return filepath.Dir(p), base
	}
	return p, ""
}

// DetectFlavors lists flavor folders that exist inside root, known ones
// first in KnownFlavors order.
func DetectFlavors(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	rank := map[string]int{}
	for i, f := range KnownFlavors {
		rank[f] = i
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && IsFlavorDir(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, iok := rank[out[i]]
		rj, jok := rank[out[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		default:
			return out[i] < out[j]
		}
	})
	return out, nil
}

// DefaultInstallRoot is the usual WoW install location for this OS.
func DefaultInstallRoot() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Applications/World of Warcraft"
	case "windows":
		return `C:\Program Files (x86)\World of Warcraft`
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Games", "world-of-warcraft", "drive_c", "Program Files (x86)", "World of Warcraft")
	}
}
