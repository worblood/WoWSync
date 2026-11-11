package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/worblood/WoWSync/internal/wow"
)

func TestDefaultsAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "config.json")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeManual || !reflect.DeepEqual(c.Flavors, []string{"_retail_"}) || c.WoWPath == "" {
		t.Fatalf("defaults: %+v", c)
	}
	for _, cat := range wow.Categories {
		if !c.Categories[cat.ID] {
			t.Errorf("category %s should default on", cat.ID)
		}
	}
	if !reflect.DeepEqual(c.EffectiveLocalCVars(), wow.DefaultLocalCVars) {
		t.Fatal("local cvars default")
	}
	c.StorePath = `\\NAS\games\WoWSync`
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, c2) {
		t.Fatalf("round trip:\n%+v\n%+v", c, c2)
	}
}

func TestFlavorFolderAsWoWPath(t *testing.T) {
	c := &Config{WoWPath: filepath.Join("/Applications", "World of Warcraft", "_classic_")}
	c.Normalize()
	if c.WoWPath != filepath.Join("/Applications", "World of Warcraft") || !reflect.DeepEqual(c.Flavors, []string{"_classic_"}) {
		t.Fatalf("got %q %v", c.WoWPath, c.Flavors)
	}
	fl := c.FlavorList()
	if len(fl) != 1 || fl[0].Key != "classic" || fl[0].Path != filepath.Join(c.WoWPath, "_classic_") {
		t.Fatalf("flavors: %+v", fl)
	}
}

func TestSetAndValidate(t *testing.T) {
	c := Default()
	if err := c.Validate(); err == nil {
		t.Fatal("missing store path must fail validation")
	}
	steps := [][2]string{
		{"store_path", "/Volumes/nas/WoWSync"},
		{"flavors", "_retail_, _classic_era_"},
		{"mode", "auto"},
		{"categories", "addons,savedvariables"},
		{"local_cvars", "gx*,uiScale"},
		{"max_file_size_mb", "90"},
	}
	for _, s := range steps {
		if err := c.Set(s[0], s[1]); err != nil {
			t.Fatalf("set %s: %v", s[0], err)
		}
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeAuto || len(c.Flavors) != 2 || c.MaxFileSizeMB != 90 {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(c.EnabledCategories(), []string{"addons", "savedvariables"}) {
		t.Fatalf("categories: %v", c.EnabledCategories())
	}
	if err := c.Set("local_cvars", "default"); err != nil || c.LocalCVars != nil {
		t.Fatal("reset local cvars")
	}
	for _, bad := range [][2]string{{"mode", "sometimes"}, {"categories", "nope"}, {"backup_keep", "x"}, {"nope", "1"}} {
		if err := c.Set(bad[0], bad[1]); err == nil {
			t.Errorf("set %v should fail", bad)
		}
	}
	c.Flavors = []string{"retail"}
	if err := c.Validate(); err == nil {
		t.Fatal("invalid flavor must fail")
	}
}

func TestPathsHome(t *testing.T) {
	t.Setenv("WOWSYNC_HOME", "/tmp/wowsync-home")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigFile != filepath.Join("/tmp/wowsync-home", "config.json") || p.BackupDir != filepath.Join("/tmp/wowsync-home", "backups") {
		t.Fatalf("%+v", p)
	}
}
