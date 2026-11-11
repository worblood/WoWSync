package wow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRelAndFold(t *testing.T) {
	nfd := "WTF/Account/A/Ragnarós/Char" // o + combining acute
	nfc := "WTF/Account/A/Ragnarós/Char"
	if got := NormalizeRel(nfd); got != nfc {
		t.Fatalf("NFC: got %q want %q", got, nfc)
	}
	if got := NormalizeRel(`./WTF//Account/../Account/x.lua`); got != "WTF/Account/x.lua" {
		t.Fatalf("clean: %q", got)
	}
	if Fold("WTF/Account/ABC/SavedVariables/Foo.lua") != Fold("wtf/account/abc/savedvariables/foo.LUA") {
		t.Fatal("fold should be case-insensitive")
	}
}

func TestValidateRel(t *testing.T) {
	good := []string{"retail/WTF/Config.wtf", "retail/WTF/Account/X/Argent Dawn/Bob/AddOns.txt"}
	for _, p := range good {
		if err := ValidateRel(p); err != nil {
			t.Errorf("%q: unexpected %v", p, err)
		}
	}
	bad := []string{"", "/abs", "../x", "a/../b", `a\b`, "a/b:c", "a/CON", "a/nul.txt", "a/b.", "a/b ", "a//b", "a/<x>"}
	for _, p := range bad {
		if err := ValidateRel(p); err == nil {
			t.Errorf("%q: expected error", p)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"Interface/AddOns/**", "Interface/AddOns/WeakAuras/WeakAuras.toc", true},
		{"Interface/AddOns/**", "interface/addons/x", true},
		{"WTF/Account/*/SavedVariables/**", "WTF/Account/ACC/SavedVariables/Foo.lua", true},
		{"WTF/Account/*/SavedVariables/**", "WTF/Account/ACC/Realm/Char/SavedVariables/Foo.lua", false},
		{"WTF/Account/*/*/*/SavedVariables/**", "WTF/Account/ACC/Realm/Char/SavedVariables/Foo.lua", true},
		{"WTF/Account/**/bindings-cache.wtf", "WTF/Account/ACC/bindings-cache.wtf", true},
		{"WTF/Account/**/bindings-cache.wtf", "WTF/Account/ACC/R/C/bindings-cache.wtf", true},
		{"WTF/**", "WTF/Config.wtf", true},
		{"WTF/**", "Interface/x", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.name); got != c.want {
			t.Errorf("MatchGlob(%q,%q)=%v want %v", c.pat, c.name, got, c.want)
		}
	}
}

func TestExcluder(t *testing.T) {
	e := NewExcluder(DefaultExcludes, []string{"Interface/AddOns/DevTool/", "# comment", ""})
	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"Cache", true, true},
		{"Cache/WDB/x.wdb", false, true},
		{"Interface/AddOns/Foo/Cache/x.lua", false, false}, // /Cache/ is anchored
		{"Logs", true, true},
		{"WTF/Account/A/SavedVariables/Foo.lua.bak", false, true},
		{"WTF/Account/A/SavedVariables/Foo.lua", false, false},
		{"Interface/AddOns/Foo/.git/config", false, true},
		{"Interface/AddOns/Foo/.DS_Store", false, true},
		{"Interface/AddOns/Foo/._Foo.lua", false, true},
		{"WTF/Account/A/cache.md5", false, true},
		{"Interface/AddOns/DevTool/DevTool.toc", false, true},
		{"Interface/AddOns/DevToolbox/x.toc", false, false},
		{"WTF/Account/A/SavedVariables/.git", false, false}, // .git/ is dir-only
	}
	for _, c := range cases {
		if got := e.Excluded(c.rel, c.isDir); got != c.want {
			t.Errorf("Excluded(%q,%v)=%v want %v", c.rel, c.isDir, got, c.want)
		}
	}
}

func TestClassifyAndRules(t *testing.T) {
	cases := map[string]string{
		"Interface/AddOns/WeakAuras/WeakAuras.lua":              "addons",
		"WTF/Account/ACC/Realm/Char/AddOns.txt":                 "addons",
		"WTF/Account/ACC/SavedVariables/WeakAuras.lua":          "savedvariables",
		"WTF/Account/ACC/Realm/Char/SavedVariables/Details.lua": "savedvariables",
		"WTF/Account/ACC/bindings-cache.wtf":                    "keybindings",
		"WTF/Account/ACC/Realm/Char/macros-cache.txt":           "macros",
		"WTF/Account/ACC/Realm/Char/chat-cache.txt":             "chat",
		"WTF/Account/ACC/config-cache.wtf":                      "gamesettings",
		"WTF/Account/ACC/Realm/Char/layout-local.txt":           "gamesettings",
		"WTF/Config.wtf":                                        "config",
		"WTF/Account/ACC/Realm/Char/something.txt":              "other",
		"Fonts/x.ttf":                                           "",
	}
	for rel, want := range cases {
		if got := Classify(rel); got != want {
			t.Errorf("Classify(%q)=%q want %q", rel, got, want)
		}
	}

	cats := DefaultCategories()
	cats["macros"] = false
	r := NewRules(cats, nil)
	if !r.Managed("WTF/Account/ACC/SavedVariables/WeakAuras.lua") {
		t.Error("savedvariables should be managed")
	}
	if r.Managed("WTF/Account/ACC/macros-cache.txt") {
		t.Error("disabled category must not be managed")
	}
	if r.Managed(ConfigWTF) {
		t.Error("Config.wtf is merged per CVar, never managed as file")
	}
	if r.Managed("WTF/Account/ACC/SavedVariables/WeakAuras.lua.bak") {
		t.Error("excluded file must not be managed")
	}
}

func TestFlavors(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"_classic_era_", "_retail_", "_weird_", "Data", "_classic_"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := DetectFlavors(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"_retail_", "_classic_", "_classic_era_", "_weird_"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectFlavors=%v want %v", got, want)
	}
	r, f := SplitInstallPath(filepath.Join(root, "_retail_"))
	if r != root || f != "_retail_" {
		t.Fatalf("SplitInstallPath=%q,%q", r, f)
	}
	if r, f := SplitInstallPath(root); r != root || f != "" {
		t.Fatalf("SplitInstallPath(root)=%q,%q", r, f)
	}
	if FlavorKey("_classic_era_") != "classic_era" {
		t.Fatal("FlavorKey")
	}
}

func TestIsWoWProcess(t *testing.T) {
	yes := []string{"Wow.exe", "WowClassic.exe", "WOWT.EXE",
		"/Applications/World of Warcraft/_retail_/World of Warcraft.app/Contents/MacOS/World of Warcraft",
		"World of Warcraft Classic", "World of Warcraf"}
	no := []string{"WowUp.exe", "WowUp-CF.exe", "Battle.net.exe", "Battle.net", "World of Warcraft Launcher", "wowsync"}
	for _, n := range yes {
		if !IsWoWProcess(n) {
			t.Errorf("%q should match", n)
		}
	}
	for _, n := range no {
		if IsWoWProcess(n) {
			t.Errorf("%q should not match", n)
		}
	}
	if _, err := RunningClients(); err != nil {
		t.Fatalf("RunningClients: %v", err)
	}
}

func TestConfigWTFRoundTripAndMerge(t *testing.T) {
	local := "SET gxApi \"D3D12\"\r\nSET Sound_MasterVolume \"0.5\"\r\nSET autoLootDefault \"0\"\r\nSET oldThing \"1\"\r\n# comment\r\n"
	lines, crlf := ParseConfigWTF([]byte(local))
	if !crlf || len(lines) != 5 {
		t.Fatalf("parse: crlf=%v n=%d", crlf, len(lines))
	}
	if string(FormatConfigWTF(lines, crlf)) != local {
		t.Fatalf("round trip mismatch:\n%q", FormatConfigWTF(lines, crlf))
	}

	m := CVarMatcher(DefaultLocalCVars)
	synced := SyncedCVars(lines, m)
	if _, ok := synced["gxApi"]; ok {
		t.Fatal("gxApi must stay local")
	}
	if synced["autoLootDefault"] != "0" {
		t.Fatalf("synced=%v", synced)
	}

	remote := map[string]string{"gxApi": "metal", "autoLootDefault": "1", "Sound_MasterVolume": "0.5", "newCVar": "x"}
	merged, changes := MergeCVars(lines, remote, m.Match)
	out := string(FormatConfigWTF(merged, crlf))
	want := "SET gxApi \"D3D12\"\r\nSET Sound_MasterVolume \"0.5\"\r\nSET autoLootDefault \"1\"\r\n# comment\r\nSET newCVar \"x\"\r\n"
	if out != want {
		t.Fatalf("merged:\n%q\nwant\n%q", out, want)
	}
	kinds := map[string]ChangeKind{}
	for _, c := range changes {
		kinds[c.Name] = c.Kind
	}
	wantKinds := map[string]ChangeKind{"autoLootDefault": Modified, "oldThing": Removed, "newCVar": Added}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("changes=%v", kinds)
	}
	if !strings.Contains(out, "D3D12") {
		t.Fatal("protected value overwritten")
	}
}
