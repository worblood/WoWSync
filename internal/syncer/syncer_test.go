package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worblood/WoWSync/internal/config"
	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/wow"
)

var ctx = context.Background()

type machine struct {
	t       *testing.T
	eng     *Engine
	root    string
	running []string
}

func newStore(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "nas", "WoWSync")
	os.MkdirAll(filepath.Dir(dir), 0o755)
	if _, err := store.InitFolder(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newMachine(t *testing.T, storeDir, name string, flavors ...string) *machine {
	t.Helper()
	if len(flavors) == 0 {
		flavors = []string{"_retail_"}
	}
	m := &machine{t: t, root: t.TempDir()}
	for _, f := range flavors {
		os.MkdirAll(filepath.Join(m.root, f, "WTF"), 0o755)
	}
	home := t.TempDir()
	cfg := config.Default()
	cfg.StorePath = storeDir
	cfg.WoWPath = m.root
	cfg.Flavors = flavors
	cfg.MachineName = name
	m.eng = New(cfg, config.PathsIn(home, home))
	m.eng.IsWoWRunning = func() ([]string, error) { return m.running, nil }
	return m
}

func (m *machine) path(rel string) string {
	return filepath.Join(m.root, filepath.FromSlash(rel))
}

func (m *machine) write(rel, content string) {
	m.t.Helper()
	p := m.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
	// Make sure the hash cache sees a new mtime even on coarse clocks.
	future := time.Now().Add(time.Duration(len(content)+1) * time.Millisecond)
	os.Chtimes(p, future, future)
}

func (m *machine) read(rel string) string {
	m.t.Helper()
	b, err := os.ReadFile(m.path(rel))
	if err != nil {
		m.t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func (m *machine) exists(rel string) bool {
	_, err := os.Stat(m.path(rel))
	return err == nil
}

func (m *machine) push(force bool) *PushResult {
	m.t.Helper()
	plan, err := m.eng.PlanPush(ctx)
	if err != nil {
		m.t.Fatal(err)
	}
	res, err := m.eng.Push(ctx, plan, PushOptions{Force: force})
	if err != nil {
		m.t.Fatal(err)
	}
	return res
}

func (m *machine) pull() *PullResult {
	m.t.Helper()
	plan, err := m.eng.PlanPull(ctx)
	if err != nil {
		m.t.Fatal(err)
	}
	res, err := m.eng.Pull(ctx, plan, PullOptions{})
	if err != nil {
		m.t.Fatal(err)
	}
	return res
}

func (m *machine) advice() Advice {
	m.t.Helper()
	s, err := m.eng.Status(ctx)
	if err != nil {
		m.t.Fatal(err)
	}
	return s.Advice
}

const (
	sv       = "_retail_/WTF/Account/ACC/SavedVariables/WeakAuras.lua"
	charSV   = "_retail_/WTF/Account/ACC/Argent Dawn/Bob/SavedVariables/Details.lua"
	bindings = "_retail_/WTF/Account/ACC/bindings-cache.wtf"
	macros   = "_retail_/WTF/Account/ACC/macros-cache.txt"
	addonTOC = "_retail_/Interface/AddOns/WeakAuras/WeakAuras.toc"
	addonLua = "_retail_/Interface/AddOns/WeakAuras/WeakAuras.lua"
	cfgWTF   = "_retail_/WTF/Config.wtf"
)

func changed(d Diff, kind wow.ChangeKind) []string {
	var out []string
	for _, c := range d.Files {
		if c.Kind == kind {
			out = append(out, c.Path)
		}
	}
	return out
}

func TestPushPullRoundTrip(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "MacBook")
	b := newMachine(t, storeDir, "Gaming PC")

	a.write(sv, "WeakAurasSaved = {\r\n  x = 1,\r\n}\r\n")
	a.write(charSV, "Details = {}\n")
	a.write(bindings, "bind X\n")
	a.write(addonTOC, "## Title: WeakAuras\n")
	a.write(addonLua, "print('hi')\n")
	a.write(cfgWTF, "SET gxApi \"metal\"\nSET autoLootDefault \"1\"\nSET uiScale \"0.7\"\n")
	a.write("_retail_/WTF/Account/ACC/SavedVariables/WeakAuras.lua.bak", "old")
	a.write("_retail_/Cache/WDB/creaturecache.wdb", "cache")
	a.write("_retail_/Interface/AddOns/WeakAuras/.DS_Store", "junk")

	if got := a.advice(); got != AdviceEmptyStore {
		t.Fatalf("advice on empty store = %s", got)
	}
	plan, err := a.eng.PlanPush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := plan.Diff.Count(wow.Added); n != 5 {
		t.Fatalf("push adds %d files, want 5: %v", n, changed(plan.Diff, wow.Added))
	}
	if plan.Conflict {
		t.Fatal("no conflict expected on empty store")
	}
	res, err := a.eng.Push(ctx, plan, PushOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Snapshot == nil || res.Uploaded != 5 {
		t.Fatalf("push result %+v", res)
	}
	if got := a.advice(); got != AdviceInSync {
		t.Fatalf("A advice after push = %s", got)
	}
	if r := a.push(false); !r.NoChanges {
		t.Fatal("second push should have nothing to do")
	}

	b.write(cfgWTF, "SET gxApi \"D3D12\"\nSET autoLootDefault \"0\"\nSET uiScale \"1.0\"\n")
	if got := b.advice(); got != AdviceFirstSync {
		t.Fatalf("B advice = %s", got)
	}
	pp, err := b.eng.PlanPull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := pp.Diff.Count(wow.Added); n != 5 {
		t.Fatalf("pull adds %d files: %v", n, changed(pp.Diff, wow.Added))
	}
	if pp.Diff.CVarCount() != 1 || pp.Diff.CVars[0].Changes[0].Name != "autoLootDefault" {
		t.Fatalf("cvar diff: %+v", pp.Diff.CVars)
	}
	pr, err := b.eng.Pull(ctx, pp, PullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Written != 5 || pr.Backup == nil {
		t.Fatalf("pull result %+v", pr)
	}
	if got := b.read(sv); got != "WeakAurasSaved = {\n  x = 1,\n}\n" {
		t.Fatalf("pulled content (line endings normalized) = %q", got)
	}
	if got := b.read(cfgWTF); got != "SET gxApi \"D3D12\"\nSET autoLootDefault \"1\"\nSET uiScale \"1.0\"\n" {
		t.Fatalf("merged Config.wtf = %q", got)
	}
	if b.exists("_retail_/WTF/Account/ACC/SavedVariables/WeakAuras.lua.bak") || b.exists("_retail_/Cache/WDB/creaturecache.wdb") {
		t.Fatal("excluded files must not be synced")
	}
	for _, m := range []*machine{a, b} {
		if got := m.advice(); got != AdviceInSync {
			t.Fatalf("%s advice = %s", m.eng.Cfg.MachineName, got)
		}
	}
}

func TestPullBlockedWhileWoWRuns(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")
	b := newMachine(t, storeDir, "B")
	a.write(sv, "x")
	a.push(false)

	b.running = []string{"Wow.exe"}
	plan, err := b.eng.PlanPull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Running) == 0 || !strings.Contains(strings.Join(plan.Warnings, "\n"), "running") {
		t.Fatalf("plan should warn about WoW: %v", plan.Warnings)
	}
	if _, err := b.eng.Pull(ctx, plan, PullOptions{}); !errors.Is(err, ErrWoWRunning) {
		t.Fatalf("want ErrWoWRunning, got %v", err)
	}
	if b.exists(sv) {
		t.Fatal("nothing may be written while WoW runs")
	}
	if _, err := b.eng.Pull(ctx, plan, PullOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !b.exists(sv) {
		t.Fatal("forced pull should write")
	}
}

func TestConflictAndForce(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")
	b := newMachine(t, storeDir, "B")
	a.write(sv, "v1")
	a.push(false)
	b.pull()

	a.write(sv, "v2-from-A")
	a.push(false)
	b.write(sv, "v2-from-B")

	if got := b.advice(); got != AdviceConflict {
		t.Fatalf("B advice = %s", got)
	}
	plan, err := b.eng.PlanPush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Conflict {
		t.Fatal("conflict expected")
	}
	if _, err := b.eng.Push(ctx, plan, PushOptions{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if _, err := b.eng.Push(ctx, plan, PushOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := a.advice(); got != AdvicePull {
		t.Fatalf("A advice = %s", got)
	}
	a.pull()
	if got := a.read(sv); got != "v2-from-B" {
		t.Fatalf("A has %q", got)
	}
}

func TestRemoveBackupAndRestore(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")
	b := newMachine(t, storeDir, "B")
	a.write(sv, "v1")
	a.write(addonTOC, "toc")
	a.push(false)
	b.pull()
	firstSync := b.eng.LoadState().LastSnapshot

	os.RemoveAll(a.path("_retail_/Interface/AddOns/WeakAuras"))
	a.write(sv, "v2")
	a.write(macros, "MACRO 1")
	a.push(false)

	plan, err := b.eng.PlanPull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot paths use the flavor key ("retail/…").
	if got := changed(plan.Diff, wow.Removed); len(got) != 1 || got[0] != "retail/Interface/AddOns/WeakAuras/WeakAuras.toc" {
		t.Fatalf("removed = %v", got)
	}
	res, err := b.eng.Pull(ctx, plan, PullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b.exists(addonTOC) || b.exists("_retail_/Interface/AddOns/WeakAuras") {
		t.Fatal("removed addon (and its empty folder) should be gone")
	}
	if !b.exists("_retail_/Interface/AddOns") {
		t.Fatal("AddOns folder itself must stay")
	}
	if b.read(sv) != "v2" || b.read(macros) != "MACRO 1" {
		t.Fatal("pull did not apply")
	}

	list, err := b.eng.ListBackups()
	if err != nil || len(list) != 2 || list[0].ID != res.Backup.ID {
		t.Fatalf("backups: %+v %v", list, err)
	}
	rr, err := b.eng.Restore(ctx, "latest", RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rr.Removed != 1 || rr.Safety == nil {
		t.Fatalf("restore result %+v", rr)
	}
	if b.read(sv) != "v1" || b.read(addonTOC) != "toc" || b.exists(macros) {
		t.Fatal("restore did not bring back the pre-pull state")
	}
	if got := b.eng.LoadState().LastSnapshot; got != firstSync {
		t.Fatalf("state after restore = %s, want %s", got, firstSync)
	}
	if got := b.advice(); got != AdvicePull {
		t.Fatalf("advice after restore = %s", got)
	}
	// Undo the restore with the safety backup.
	if _, err := b.eng.Restore(ctx, rr.Safety.ID, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if b.read(sv) != "v2" || b.read(macros) != "MACRO 1" || b.exists(addonTOC) {
		t.Fatal("restoring the safety backup should undo the restore")
	}
	if err := b.eng.PruneBackups(1); err != nil {
		t.Fatal(err)
	}
	if list, _ := b.eng.ListBackups(); len(list) != 1 {
		t.Fatalf("prune kept %d", len(list))
	}
}

func TestLineEndingsAndLetterCase(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")
	b := newMachine(t, storeDir, "B")
	a.write(sv, "line1\r\nline2\r\n")
	a.write(bindings, "bind\n")
	a.push(false)

	// B has the same content with LF and a different spelling of the name.
	b.write(strings.Replace(sv, "WeakAuras.lua", "weakauras.lua", 1), "line1\nline2\n")
	b.write(strings.Replace(bindings, "ACC", "acc", 1), "bind-old\n")
	plan, err := b.eng.PlanPull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(plan.Diff.Files); n != 1 || plan.Diff.Files[0].Kind != wow.Modified {
		t.Fatalf("diff = %+v", plan.Diff.Files)
	}
	if _, err := b.eng.Pull(ctx, plan, PullOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := b.read(strings.Replace(bindings, "ACC", "acc", 1)); got != "bind\n" {
		t.Fatalf("pull should write to the local spelling, got %q", got)
	}
}

func TestScopeIsPreserved(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")                          // retail only
	b := newMachine(t, storeDir, "B", "_retail_", "_classic_") // retail + classic
	b.write("_classic_/WTF/Account/ACC/SavedVariables/Questie.lua", "q")
	b.write(macros, "B macros")
	b.write(sv, "b")
	b.push(false)

	a.eng.Cfg.Categories["macros"] = false
	a.write(macros, "A macros (local only)")
	a.pull()
	if a.read(macros) != "A macros (local only)" {
		t.Fatal("disabled category must not be touched by pull")
	}
	a.write(sv, "a")
	a.push(false)

	snap, err := b.eng.Store.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, f := range snap.Files {
		paths[f.Path] = f.Hash
	}
	if _, ok := paths["classic/WTF/Account/ACC/SavedVariables/Questie.lua"]; !ok {
		t.Fatal("classic files from B must be preserved by A's push")
	}
	if _, ok := paths["retail/WTF/Account/ACC/macros-cache.txt"]; !ok {
		t.Fatal("B's macros must be preserved (A does not sync macros)")
	}
	b.pull()
	if b.read(sv) != "a" || b.read(macros) != "B macros" {
		t.Fatal("B should get A's SavedVariables and keep its macros")
	}
}

func TestLargeFiles(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")
	a.eng.Cfg.MaxFileSizeMB = 1
	a.eng.Cfg.WarnFileSizeMB = 1
	big := strings.Repeat("x", 2<<20)
	a.write("_retail_/Interface/AddOns/Big/big.blp", big)
	a.write(sv, "small")
	plan, err := a.eng.PlanPush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Diff.Count(wow.Added) != 1 {
		t.Fatalf("big file should be skipped: %v", changed(plan.Diff, wow.Added))
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "above the limit") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
	a.eng.Cfg.MaxFileSizeMB = 0
	plan, _ = a.eng.PlanPush(ctx)
	if plan.Diff.Count(wow.Added) != 2 || !strings.Contains(strings.Join(plan.Warnings, "\n"), "large file") {
		t.Fatalf("big file should be synced with a warning: %v", plan.Warnings)
	}
}

func TestStoreChangedBetweenPreviewAndApply(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")
	b := newMachine(t, storeDir, "B")
	a.write(sv, "1")
	a.push(false)
	plan, err := b.eng.PlanPull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.write(sv, "2")
	a.push(false)
	if _, err := b.eng.Pull(ctx, plan, PullOptions{}); !errors.Is(err, ErrStoreChanged) {
		t.Fatalf("want ErrStoreChanged, got %v", err)
	}
}

func TestRemoteLocalCVarsAreRespected(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")
	b := newMachine(t, storeDir, "B")
	a.eng.Cfg.LocalCVars = append([]string{"Sound_*"}, wow.DefaultLocalCVars...)
	a.write(cfgWTF, "SET Sound_MasterVolume \"0.1\"\nSET autoLootDefault \"1\"\n")
	a.push(false)
	b.write(cfgWTF, "SET Sound_MasterVolume \"0.9\"\nSET autoLootDefault \"0\"\n")
	b.pull()
	if got := b.read(cfgWTF); got != "SET Sound_MasterVolume \"0.9\"\nSET autoLootDefault \"1\"\n" {
		t.Fatalf("Config.wtf = %q", got)
	}
}

func TestUnsafeSnapshotPathsIgnored(t *testing.T) {
	storeDir := newStore(t)
	a := newMachine(t, storeDir, "A")
	b := newMachine(t, storeDir, "B")
	a.write(sv, "ok")
	a.push(false)
	st, _ := a.eng.OpenStore()
	latest, _ := st.Latest(ctx)
	evil := *latest
	evil.Files = append([]store.FileEntry{{Path: "retail/WTF/../../evil.txt", Hash: latest.Files[0].Hash, Size: 2}}, latest.Files...)
	evil.ID = store.NewID(time.Now().Add(time.Second), "evil")
	if err := st.Put(ctx, &evil); err != nil {
		t.Fatal(err)
	}
	plan, err := b.eng.PlanPull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Diff.Count(wow.Added) != 1 || !strings.Contains(strings.Join(plan.Warnings, "\n"), "unsafe path") {
		t.Fatalf("diff %v warnings %v", changed(plan.Diff, wow.Added), plan.Warnings)
	}
}

func TestMissingStore(t *testing.T) {
	m := newMachine(t, filepath.Join(t.TempDir(), "not-mounted"), "A")
	if _, err := m.eng.PlanPull(ctx); !errors.Is(err, store.ErrNotInitialized) {
		t.Fatalf("want ErrNotInitialized, got %v", err)
	}
}

func TestWatchWoW(t *testing.T) {
	seq := [][]string{nil, {"Wow.exe"}, {"Wow.exe"}, nil, nil}
	var mu sync.Mutex
	i := 0
	running := func() ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		r := seq[min(i, len(seq)-1)]
		i++
		return r, nil
	}
	c, cancel := context.WithCancel(ctx)
	var events []bool
	done := make(chan struct{})
	go func() {
		WatchWoW(c, time.Millisecond, running, func(r bool) {
			events = append(events, r)
			if !r {
				cancel()
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not report exit")
	}
	if len(events) != 2 || !events[0] || events[1] {
		t.Fatalf("events = %v", events)
	}
}
