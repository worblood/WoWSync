// Package config holds the per-machine settings. The file lives in the
// user's config folder and is never part of any repository or store.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/worblood/WoWSync/internal/wow"
)

// Sync modes.
const (
	ModeManual = "manual"
	ModeAuto   = "auto"
)

// Config is the per-machine configuration.
type Config struct {
	// StorePath is the shared sync folder, e.g. \\NAS\games\WoWSync or
	// /Volumes/games/WoWSync.
	StorePath string `json:"store_path"`
	// WoWPath is the WoW install folder (the one containing _retail_ …).
	WoWPath string `json:"wow_path"`
	// Flavors are the flavor folders to sync, e.g. ["_retail_", "_classic_"].
	Flavors []string `json:"flavors"`
	// Mode is "manual" (default) or "auto".
	Mode string `json:"mode"`
	// MachineName identifies this computer in snapshots.
	MachineName string `json:"machine_name"`
	// Categories switches sync categories on/off; missing ids are on.
	Categories map[string]bool `json:"categories"`
	// LocalCVars are Config.wtf CVar patterns kept local; nil = defaults.
	LocalCVars []string `json:"local_cvars,omitempty"`
	// ExtraExcludes are additional exclude patterns (see wow.Excluder).
	ExtraExcludes []string `json:"extra_excludes,omitempty"`
	// WarnFileSizeMB warns about files larger than this.
	WarnFileSizeMB int `json:"warn_file_size_mb"`
	// MaxFileSizeMB skips files larger than this (0 = no limit).
	MaxFileSizeMB int `json:"max_file_size_mb"`
	// BackupKeep is the number of local backups kept.
	BackupKeep int `json:"backup_keep"`
	// SnapshotKeep is the number of snapshots prune keeps in the store.
	SnapshotKeep int `json:"snapshot_keep"`
	// AutoPushDelaySec is how long to wait after WoW exits before pushing.
	AutoPushDelaySec int `json:"auto_push_delay_seconds"`
}

// Default returns a configuration with defaults for this OS.
func Default() *Config {
	c := &Config{}
	c.Normalize()
	return c
}

// Normalize fills in defaults and accepts a flavor folder as WoWPath.
func (c *Config) Normalize() {
	if c.WoWPath == "" {
		c.WoWPath = wow.DefaultInstallRoot()
	}
	if root, flavor := wow.SplitInstallPath(c.WoWPath); flavor != "" {
		c.WoWPath = root
		if !contains(c.Flavors, flavor) {
			c.Flavors = append(c.Flavors, flavor)
		}
	}
	if len(c.Flavors) == 0 {
		c.Flavors = []string{"_retail_"}
	}
	if c.Mode != ModeAuto {
		c.Mode = ModeManual
	}
	if c.MachineName == "" {
		c.MachineName = DefaultMachineName()
	}
	if c.Categories == nil {
		c.Categories = map[string]bool{}
	}
	for _, cat := range wow.Categories {
		if _, ok := c.Categories[cat.ID]; !ok {
			c.Categories[cat.ID] = true
		}
	}
	if c.WarnFileSizeMB <= 0 {
		c.WarnFileSizeMB = 20
	}
	if c.MaxFileSizeMB < 0 {
		c.MaxFileSizeMB = 0
	}
	if c.BackupKeep <= 0 {
		c.BackupKeep = 20
	}
	if c.SnapshotKeep <= 0 {
		c.SnapshotKeep = 50
	}
	if c.AutoPushDelaySec <= 0 {
		c.AutoPushDelaySec = 10
	}
}

// Validate reports settings that prevent syncing.
func (c *Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.StorePath) == "" {
		errs = append(errs, errors.New("sync folder (store_path) is not set"))
	}
	if strings.TrimSpace(c.WoWPath) == "" {
		errs = append(errs, errors.New("WoW folder (wow_path) is not set"))
	}
	for _, f := range c.Flavors {
		if !wow.IsFlavorDir(f) {
			errs = append(errs, fmt.Errorf("invalid flavor folder %q (expected e.g. _retail_)", f))
		}
	}
	return errors.Join(errs...)
}

// FlavorList returns the configured flavors with absolute paths.
func (c *Config) FlavorList() []wow.Flavor {
	out := make([]wow.Flavor, 0, len(c.Flavors))
	for _, d := range c.Flavors {
		out = append(out, wow.NewFlavor(c.WoWPath, d))
	}
	return out
}

// EffectiveLocalCVars returns the CVar patterns kept local.
func (c *Config) EffectiveLocalCVars() []string {
	if c.LocalCVars == nil {
		return wow.DefaultLocalCVars
	}
	return c.LocalCVars
}

// Rules builds the file rules for this machine.
func (c *Config) Rules() *wow.Rules {
	return wow.NewRules(c.Categories, c.ExtraExcludes)
}

// DefaultMachineName returns the host name without ".local".
func DefaultMachineName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return runtime.GOOS
	}
	return strings.TrimSuffix(h, ".local")
}

// Load reads the config file; a missing file yields defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	c.Normalize()
	return c, nil
}

// Save writes the config file atomically.
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'))
}

// Keys lists the settings accepted by Set.
var Keys = []string{
	"store_path", "wow_path", "flavors", "mode", "machine_name", "categories",
	"local_cvars", "extra_excludes", "warn_file_size_mb", "max_file_size_mb",
	"backup_keep", "snapshot_keep", "auto_push_delay_seconds",
}

// Set changes one setting from a string (used by the CLI). Lists are comma
// separated; "categories" takes the enabled ids, "local_cvars" accepts
// "default" to restore the built-in list.
func (c *Config) Set(key, value string) error {
	value = strings.TrimSpace(value)
	atoi := func() (int, error) {
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("%s: %q is not a number", key, value)
		}
		return n, nil
	}
	var err error
	switch key {
	case "store_path":
		c.StorePath = value
	case "wow_path":
		c.WoWPath = value
	case "flavors":
		c.Flavors = splitList(value)
	case "mode":
		if value != ModeManual && value != ModeAuto {
			return fmt.Errorf("mode must be %q or %q", ModeManual, ModeAuto)
		}
		c.Mode = value
	case "machine_name":
		c.MachineName = value
	case "categories":
		enabled := splitList(value)
		for _, id := range enabled {
			if _, ok := wow.CategoryByID(id); !ok {
				return fmt.Errorf("unknown category %q", id)
			}
		}
		c.Categories = map[string]bool{}
		for _, cat := range wow.Categories {
			c.Categories[cat.ID] = contains(enabled, cat.ID)
		}
	case "local_cvars":
		if value == "default" {
			c.LocalCVars = nil
		} else {
			c.LocalCVars = splitList(value)
		}
	case "extra_excludes":
		c.ExtraExcludes = splitList(value)
	case "warn_file_size_mb":
		c.WarnFileSizeMB, err = atoi()
	case "max_file_size_mb":
		c.MaxFileSizeMB, err = atoi()
	case "backup_keep":
		c.BackupKeep, err = atoi()
	case "snapshot_keep":
		c.SnapshotKeep, err = atoi()
	case "auto_push_delay_seconds":
		c.AutoPushDelaySec, err = atoi()
	default:
		return fmt.Errorf("unknown setting %q (known: %s)", key, strings.Join(Keys, ", "))
	}
	if err != nil {
		return err
	}
	c.Normalize()
	return nil
}

// EnabledCategories returns the ids of enabled categories in display order.
func (c *Config) EnabledCategories() []string {
	var out []string
	for _, cat := range wow.Categories {
		if c.Categories[cat.ID] {
			out = append(out, cat.ID)
		}
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
