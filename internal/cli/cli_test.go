package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/worblood/WoWSync/internal/config"
	"github.com/worblood/WoWSync/internal/syncer"
)

type env struct {
	t       *testing.T
	paths   config.Paths
	wow     string
	running []string
}

func newEnv(t *testing.T) *env {
	home := t.TempDir()
	e := &env{t: t, paths: config.PathsIn(home, home), wow: t.TempDir()}
	os.MkdirAll(filepath.Join(e.wow, "_retail_", "WTF"), 0o755)
	return e
}

func (e *env) run(stdin string, args ...string) (int, string) {
	e.t.Helper()
	var out bytes.Buffer
	app := &App{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &out, Paths: &e.paths,
		Setup: func(en *syncer.Engine) {
			en.IsWoWRunning = func() ([]string, error) { return e.running, nil }
		},
	}
	code := app.Run(args)
	return code, out.String()
}

func (e *env) write(rel, content string) {
	p := filepath.Join(e.wow, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func must(t *testing.T, code int, out string, want ...string) {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestCLIFlow(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "WoWSync")
	a, b := newEnv(t), newEnv(t)
	const sv = "_retail_/WTF/Account/ACC/SavedVariables/WeakAuras.lua"
	a.write(sv, "WeakAurasSaved = {}\n")

	if code, out := a.run("", "status"); code == 0 {
		t.Fatalf("status without store should fail:\n%s", out)
	}
	code, out := a.run("", "config", "set", "wow_path", filepath.Join(a.wow, "_retail_"))
	must(t, code, out, "Saved wow_path")
	code, out = a.run("", "init-store", storeDir)
	must(t, code, out, "Store ready")
	code, out = a.run("", "push")
	must(t, code, out, "+ retail/WTF/Account/ACC/SavedVariables/WeakAuras.lua", "Pushed snapshot")
	code, out = a.run("", "push")
	must(t, code, out, "Nothing to push")

	b.run("", "config", "set", "wow_path", b.wow)
	b.run("", "config", "set", "store_path", storeDir)
	code, out = b.run("", "status")
	must(t, code, out, "First sync", "Pull would: 1 added")

	b.running = []string{"Wow.exe"}
	if code, out := b.run("", "pull", "-y"); code == 0 || !strings.Contains(out, "running") {
		t.Fatalf("pull while WoW runs must fail:\n%s", out)
	}
	b.running = nil

	code, out = b.run("n\n", "pull")
	if code == 0 || !strings.Contains(out, "Cancelled") {
		t.Fatalf("declined pull should cancel:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(b.wow, filepath.FromSlash(sv))); err == nil {
		t.Fatal("declined pull must not write")
	}
	code, out = b.run("y\n", "pull")
	must(t, code, out, "Back up and apply", "Done: 1 files written", "Backup:")
	code, out = b.run("", "status")
	must(t, code, out, "In sync")

	code, out = b.run("", "backups")
	must(t, code, out, "pre-pull")
	code, out = b.run("", "restore", "latest", "-y")
	must(t, code, out, "Restored", "removed 1")
	code, out = b.run("", "snapshots")
	must(t, code, out, "1 snapshot(s)")
	code, out = b.run("", "prune", "--keep", "5")
	must(t, code, out, "Removed 0 snapshot(s)")
	code, out = b.run("", "config", "categories")
	must(t, code, out, "savedvariables", "[on ]")
	if code, _ := b.run("", "config", "set", "mode", "sometimes"); code == 0 {
		t.Fatal("invalid mode must fail")
	}
	if code, _ := b.run("", "bogus"); code != 2 {
		t.Fatal("unknown command should exit 2")
	}
}

func TestCLIConflict(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "WoWSync")
	a, b := newEnv(t), newEnv(t)
	const sv = "_retail_/WTF/Account/ACC/SavedVariables/X.lua"
	for _, e := range []*env{a, b} {
		e.run("", "config", "set", "wow_path", e.wow)
		e.run("", "config", "set", "store_path", storeDir)
	}
	a.run("", "init-store")
	a.write(sv, "a1")
	code, out := a.run("", "push")
	must(t, code, out, "Pushed snapshot")
	b.write(sv, "b1")
	code, out = b.run("", "push")
	if code == 0 || !strings.Contains(out, "newer changes") {
		t.Fatalf("push should report conflict:\n%s", out)
	}
	code, out = b.run("", "push", "--force")
	must(t, code, out, "Pushed snapshot")
}
