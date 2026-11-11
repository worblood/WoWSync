// Package cli implements the wowsync command line.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/worblood/WoWSync/internal/config"
	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/syncer"
	"github.com/worblood/WoWSync/internal/version"
	"github.com/worblood/WoWSync/internal/wow"
)

// App holds the I/O and hooks of one CLI invocation.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Paths  *config.Paths // nil = config.DefaultPaths()
	// Setup can adjust the engine (tests).
	Setup func(*syncer.Engine)

	in      *bufio.Reader
	verbose bool
}

const usage = `WoWSync – sync World of Warcraft settings and addons between computers.

Usage:
  wowsync status                     show local changes vs. the store
  wowsync pull [-y] [-v] [--force]   preview, back up and apply the newest snapshot
  wowsync push [-v] [--force]        upload this computer's state as a new snapshot
  wowsync backups                    list local backups
  wowsync restore [ID|latest] [-y]   restore a backup (default: latest)
  wowsync snapshots                  list snapshots in the store
  wowsync prune [--keep N]           delete old snapshots and unused data
  wowsync init-store [PATH]          create a store (and save PATH as sync folder)
  wowsync config show|path|categories
  wowsync config set KEY VALUE       keys: %s
  wowsync version

Flags:
  -y, --yes     do not ask for confirmation
  -v, --verbose list every file
  --force       pull/restore: allow while WoW is running
                push: replace newer changes from another computer
`

// Run executes the command line and returns the exit code.
func (a *App) Run(args []string) int {
	if a.Stdin == nil {
		a.Stdin = os.Stdin
	}
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
	a.in = bufio.NewReader(a.Stdin)
	if len(args) == 0 {
		a.printUsage()
		return 2
	}
	ctx := context.Background()
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "status":
		err = a.status(ctx)
	case "pull":
		err = a.pull(ctx, rest)
	case "push":
		err = a.push(ctx, rest)
	case "backups":
		err = a.backups()
	case "restore":
		err = a.restore(ctx, rest)
	case "snapshots":
		err = a.snapshots(ctx)
	case "prune":
		err = a.prune(ctx, rest)
	case "init-store":
		err = a.initStore(rest)
	case "config":
		err = a.config(rest)
	case "version", "--version":
		fmt.Fprintln(a.Stdout, "wowsync", version.String())
	case "help", "-h", "--help":
		a.printUsage()
	default:
		fmt.Fprintf(a.Stderr, "unknown command %q\n\n", cmd)
		a.printUsage()
		return 2
	}
	if errors.Is(err, errCancelled) {
		fmt.Fprintln(a.Stdout, "Cancelled – nothing was changed.")
		return 1
	}
	if err != nil {
		fmt.Fprintln(a.Stderr, "error:", err)
		return 1
	}
	return 0
}

var errCancelled = errors.New("cancelled")

func (a *App) printUsage() {
	fmt.Fprintf(a.Stderr, usage, strings.Join(config.Keys, ", "))
}

func (a *App) paths() (config.Paths, error) {
	if a.Paths != nil {
		return *a.Paths, nil
	}
	return config.DefaultPaths()
}

func (a *App) load() (*config.Config, config.Paths, error) {
	p, err := a.paths()
	if err != nil {
		return nil, p, err
	}
	cfg, err := config.Load(p.ConfigFile)
	return cfg, p, err
}

func (a *App) engine() (*syncer.Engine, error) {
	cfg, p, err := a.load()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w\nset it with: wowsync config set store_path <folder> (config: %s)", err, p.ConfigFile)
	}
	e := syncer.New(cfg, p)
	if a.verbose {
		e.Logf = func(format string, args ...any) { fmt.Fprintf(a.Stderr, format+"\n", args...) }
	}
	if a.Setup != nil {
		a.Setup(e)
	}
	return e, nil
}

type flags struct {
	yes, verbose, force bool
	keep                int
	args                []string
}

func parseFlags(name string, args []string) (*flags, error) {
	f := &flags{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&f.yes, "y", false, "")
	fs.BoolVar(&f.yes, "yes", false, "")
	fs.BoolVar(&f.verbose, "v", false, "")
	fs.BoolVar(&f.verbose, "verbose", false, "")
	fs.BoolVar(&f.force, "force", false, "")
	fs.BoolVar(&f.force, "f", false, "")
	fs.IntVar(&f.keep, "keep", 0, "")
	// Allow flags after positional arguments.
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	f.args = pos
	return f, nil
}

func (a *App) confirm(question string) bool {
	fmt.Fprintf(a.Stdout, "%s [y/N] ", question)
	line, _ := a.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "j", "ja":
		return true
	}
	return false
}

func (a *App) status(ctx context.Context) error {
	e, err := a.engine()
	if err != nil {
		return err
	}
	s, err := e.Status(ctx)
	if err != nil {
		return err
	}
	w := a.Stdout
	fmt.Fprintf(w, "Store:      %s\n", e.Cfg.StorePath)
	if s.Latest != nil {
		fmt.Fprintf(w, "Newest:     %s from %s\n", s.Latest.Created.Local().Format("2006-01-02 15:04"), s.Latest.Machine)
	}
	if s.State.LastSnapshot != "" {
		fmt.Fprintf(w, "Last sync:  %s (%s)\n", s.State.LastSync.Local().Format("2006-01-02 15:04"), s.State.LastAction)
	} else {
		fmt.Fprintln(w, "Last sync:  never")
	}
	fmt.Fprintf(w, "WoW:        %s\n", runningText(s.Running))
	fmt.Fprintf(w, "Flavors:    %s  (%s)\n", strings.Join(e.Cfg.Flavors, ", "), e.Cfg.WoWPath)
	fmt.Fprintf(w, "Mode:       %s\n", e.Cfg.Mode)
	if !s.Diff.Empty() {
		fmt.Fprintf(w, "Pull would: %s\n", s.Diff.Summary())
	}
	fmt.Fprintf(w, "\n%s\n", s.Message)
	printWarnings(w, s.Warnings)
	return nil
}

func runningText(r []string) string {
	if len(r) == 0 {
		return "not running"
	}
	return "RUNNING (" + strings.Join(r, ", ") + ")"
}

func printWarnings(w io.Writer, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintln(w, "\nWarnings:")
	for _, s := range warnings {
		fmt.Fprintln(w, "  !", s)
	}
}

// PrintDiff writes a human readable preview.
func PrintDiff(w io.Writer, d syncer.Diff, verbose bool) {
	const perCategory = 15
	marks := map[wow.ChangeKind]string{wow.Added: "+", wow.Modified: "~", wow.Removed: "-"}
	for _, g := range d.ByCategory() {
		fmt.Fprintf(w, "\n%s (%d)\n", g.Category.Name, len(g.Changes))
		for i, c := range g.Changes {
			if !verbose && i == perCategory {
				fmt.Fprintf(w, "  … and %d more (use -v to list all)\n", len(g.Changes)-perCategory)
				break
			}
			size := c.NewSize
			if c.Kind == wow.Removed {
				size = c.OldSize
			}
			fmt.Fprintf(w, "  %s %s  (%s)\n", marks[c.Kind], c.Path, human(size))
		}
	}
	for _, cv := range d.CVars {
		fmt.Fprintf(w, "\nConfig.wtf – %s (%d)\n", cv.Flavor, len(cv.Changes))
		for i, c := range cv.Changes {
			if !verbose && i == perCategory {
				fmt.Fprintf(w, "  … and %d more\n", len(cv.Changes)-perCategory)
				break
			}
			switch c.Kind {
			case wow.Added:
				fmt.Fprintf(w, "  + %s = %q\n", c.Name, c.New)
			case wow.Modified:
				fmt.Fprintf(w, "  ~ %s: %q → %q\n", c.Name, c.Old, c.New)
			case wow.Removed:
				fmt.Fprintf(w, "  - %s (was %q)\n", c.Name, c.Old)
			}
		}
	}
	fmt.Fprintf(w, "\nSummary: %s\n", d.Summary())
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func (a *App) pull(ctx context.Context, args []string) error {
	f, err := parseFlags("pull", args)
	if err != nil {
		return err
	}
	a.verbose = f.verbose
	e, err := a.engine()
	if err != nil {
		return err
	}
	plan, err := e.PlanPull(ctx)
	if err != nil {
		return err
	}
	if plan.Snapshot == nil {
		return syncer.ErrEmptyStore
	}
	w := a.Stdout
	fmt.Fprintf(w, "Newest snapshot: %s from %s\n", plan.Snapshot.Created.Local().Format("2006-01-02 15:04"), plan.Snapshot.Machine)
	if plan.Diff.Empty() {
		printWarnings(w, plan.Warnings)
		if _, err := e.Pull(ctx, plan, syncer.PullOptions{Force: f.force}); err != nil && !errors.Is(err, syncer.ErrWoWRunning) {
			return err
		}
		fmt.Fprintln(w, "Already up to date.")
		return nil
	}
	PrintDiff(w, plan.Diff, f.verbose)
	printWarnings(w, plan.Warnings)
	if len(plan.Running) > 0 && !f.force {
		return fmt.Errorf("%w (%s); use --force to pull anyway", syncer.ErrWoWRunning, strings.Join(plan.Running, ", "))
	}
	if !f.yes && !a.confirm("\nBack up and apply these changes?") {
		return errCancelled
	}
	res, err := e.Pull(ctx, plan, syncer.PullOptions{Force: f.force})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Done: %d files written, %d removed, %d CVars changed.\nBackup: %s\n", res.Written, res.Removed, res.CVars, res.Backup.ID)
	return nil
}

func (a *App) push(ctx context.Context, args []string) error {
	f, err := parseFlags("push", args)
	if err != nil {
		return err
	}
	a.verbose = f.verbose
	e, err := a.engine()
	if err != nil {
		return err
	}
	plan, err := e.PlanPush(ctx)
	if err != nil {
		return err
	}
	w := a.Stdout
	if plan.Diff.Empty() && plan.Latest != nil {
		if _, err := e.Push(ctx, plan, syncer.PushOptions{}); err != nil {
			return err
		}
		fmt.Fprintln(w, "Nothing to push – the store already has this computer's state.")
		return nil
	}
	PrintDiff(w, plan.Diff, f.verbose)
	printWarnings(w, plan.Warnings)
	if plan.Conflict && !f.force {
		return fmt.Errorf("%w. Run 'wowsync pull' first, or 'wowsync push --force' to replace them", syncer.ErrConflict)
	}
	fmt.Fprintln(w, "\nUploading…")
	res, err := e.Push(ctx, plan, syncer.PushOptions{Force: f.force})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Pushed snapshot %s (%d new files, %s uploaded).\n", res.Snapshot.ID, res.Uploaded, human(res.UploadedBytes))
	return nil
}

func (a *App) backups() error {
	cfg, p, err := a.load()
	if err != nil {
		return err
	}
	e := syncer.New(cfg, p)
	list, err := e.ListBackups()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(a.Stdout, "No backups yet. A backup is made before every pull.")
		return nil
	}
	fmt.Fprintf(a.Stdout, "Backups in %s:\n", p.BackupDir)
	for _, b := range list {
		fmt.Fprintf(a.Stdout, "  %s  %-11s %5d files  %8s\n", b.ID, b.Reason, b.Files, human(b.Size))
	}
	return nil
}

func (a *App) restore(ctx context.Context, args []string) error {
	f, err := parseFlags("restore", args)
	if err != nil {
		return err
	}
	cfg, p, err := a.load()
	if err != nil {
		return err
	}
	e := syncer.New(cfg, p)
	if a.Setup != nil {
		a.Setup(e)
	}
	id := "latest"
	if len(f.args) > 0 {
		id = f.args[0]
	}
	bi, err := e.FindBackup(id)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Backup %s (%s, %d files, %s)\n", bi.ID, bi.Reason, bi.Files, bi.Created.Local().Format("2006-01-02 15:04"))
	if !f.yes && !a.confirm("Restore it? The current state is backed up first.") {
		return errCancelled
	}
	res, err := e.Restore(ctx, bi.ID, syncer.RestoreOptions{Force: f.force})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Restored %d files, removed %d. Undo with: wowsync restore %s\n", res.Restored, res.Removed, res.Safety.ID)
	return nil
}

func (a *App) snapshots(ctx context.Context) error {
	e, err := a.engine()
	if err != nil {
		return err
	}
	st, err := e.OpenStore()
	if err != nil {
		return err
	}
	list, err := st.List(ctx)
	if err != nil {
		return err
	}
	state := e.LoadState()
	fmt.Fprintf(a.Stdout, "%d snapshot(s) in %s\n", len(list), st.Describe())
	for _, s := range list {
		mark := " "
		if s.ID == state.LastSnapshot {
			mark = "*"
		}
		fmt.Fprintf(a.Stdout, " %s %s  %s\n", mark, s.Created.Local().Format("2006-01-02 15:04:05"), s.Machine)
	}
	if state.LastSnapshot != "" {
		fmt.Fprintln(a.Stdout, "\n* = last synced on this computer")
	}
	return nil
}

func (a *App) prune(ctx context.Context, args []string) error {
	f, err := parseFlags("prune", args)
	if err != nil {
		return err
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	st, err := e.OpenStore()
	if err != nil {
		return err
	}
	keep := f.keep
	if keep <= 0 {
		keep = e.Cfg.SnapshotKeep
	}
	res, err := st.Prune(ctx, keep, 24*time.Hour)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Removed %d snapshot(s) and %d unused object(s), freed %s.\n", res.SnapshotsRemoved, res.ObjectsRemoved, human(res.BytesFreed))
	return nil
}

func (a *App) initStore(args []string) error {
	cfg, p, err := a.load()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		cfg.StorePath = args[0]
	}
	if cfg.StorePath == "" {
		return errors.New("usage: wowsync init-store PATH")
	}
	f, err := store.InitFolder(cfg.StorePath)
	if err != nil {
		return err
	}
	if err := cfg.Save(p.ConfigFile); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Store ready in %s (saved in %s).\n", f.Root(), p.ConfigFile)
	return nil
}

func (a *App) config(args []string) error {
	cfg, p, err := a.load()
	if err != nil {
		return err
	}
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "show":
		data, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Fprintln(a.Stdout, string(data))
	case "path":
		fmt.Fprintln(a.Stdout, p.ConfigFile)
	case "categories":
		for _, c := range wow.Categories {
			on := "off"
			if cfg.Categories[c.ID] {
				on = "on "
			}
			fmt.Fprintf(a.Stdout, "  [%s] %-15s %s – %s\n", on, c.ID, c.Name, c.Description)
		}
		fmt.Fprintln(a.Stdout, "\nChange with: wowsync config set categories addons,savedvariables,…")
	case "set":
		if len(args) != 3 {
			return fmt.Errorf("usage: wowsync config set KEY VALUE (keys: %s)", strings.Join(config.Keys, ", "))
		}
		if err := cfg.Set(args[1], args[2]); err != nil {
			return err
		}
		if err := cfg.Save(p.ConfigFile); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Saved %s.\n", args[1])
	default:
		return fmt.Errorf("unknown config command %q", sub)
	}
	return nil
}
