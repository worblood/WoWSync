// Package gui is the Fyne desktop app: main window, tray icon and the
// automatic mode. All sync logic lives in package syncer.
package gui

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/worblood/WoWSync/internal/config"
	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/syncer"
	"github.com/worblood/WoWSync/internal/version"
)

//go:embed tray.svg
var traySVG []byte

// AppID is the application id (also used by fyne package).
const AppID = "no.worblood.wowsync"

// Options configure Run.
type Options struct {
	Icon   []byte // app icon (PNG)
	Hidden bool   // start in the tray without showing the window
}

type ui struct {
	app     fyne.App
	win     fyne.Window
	paths   config.Paths
	hasTray bool

	cfgMu sync.Mutex
	cfg   *config.Config

	busyMu sync.Mutex
	busy   bool
	ctx    context.Context

	// Sync tab
	statusCard *widget.Card
	details    *widget.Label
	wowLabel   *widget.Label
	warnings   *widget.Label
	pullBtn    *widget.Button
	pushBtn    *widget.Button
	refreshBtn *widget.Button
	progress   *widget.ProgressBarInfinite
	busyLabel  *widget.Label
	logLabel   *widget.Label
	logScroll  *container.Scroll
	logMu      sync.Mutex
	logLines   []string

	// Backups tab
	backups      []syncer.BackupInfo
	backupList   *widget.List
	backupSel    int
	restoreBtn   *widget.Button
	backupsEmpty *widget.Label

	// Settings tabs
	settings *settingsView
	what     *whatView

	// Tray
	trayMenu *fyne.Menu
	autoItem *fyne.MenuItem
}

// Run starts the desktop app and blocks until it quits.
func Run(opt Options) {
	a := app.NewWithID(AppID)
	if len(opt.Icon) > 0 {
		a.SetIcon(fyne.NewStaticResource("Icon.png", opt.Icon))
	}
	u := &ui{app: a, backupSel: -1, ctx: context.Background()}

	paths, err := config.DefaultPaths()
	if err != nil {
		paths = config.PathsIn(".", ".")
	}
	u.paths = paths
	cfg, cfgErr := config.Load(paths.ConfigFile)
	if cfgErr != nil {
		cfg = config.Default()
	}
	u.cfg = cfg

	u.win = a.NewWindow("WoWSync")
	u.win.Resize(fyne.NewSize(880, 620))
	u.win.SetContent(u.build())

	if desk, ok := a.(desktop.App); ok {
		u.hasTray = true
		u.buildTray(desk)
		u.win.SetCloseIntercept(func() { u.win.Hide() })
	}
	a.Lifecycle().SetOnStarted(func() {
		if cfgErr != nil {
			dialog.ShowError(fmt.Errorf("could not read settings, using defaults: %w", cfgErr), u.win)
		}
		u.onStarted()
	})
	if !opt.Hidden || !u.hasTray {
		u.win.Show()
	}
	a.Run()
}

func (u *ui) config() config.Config {
	u.cfgMu.Lock()
	defer u.cfgMu.Unlock()
	c := *u.cfg
	return c
}

func (u *ui) setConfig(c *config.Config) error {
	c.Normalize()
	if err := c.Save(u.paths.ConfigFile); err != nil {
		return err
	}
	u.cfgMu.Lock()
	u.cfg = c
	u.cfgMu.Unlock()
	u.updateTrayMode()
	return nil
}

func (u *ui) engine() *syncer.Engine {
	c := u.config()
	e := syncer.New(&c, u.paths)
	e.Logf = u.logf
	return e
}

func (u *ui) build() fyne.CanvasObject {
	u.settings = newSettingsView(u)
	u.what = newWhatView(u)
	tabs := container.NewAppTabs(
		container.NewTabItemWithIcon("Sync", theme.HomeIcon(), u.buildSyncTab()),
		container.NewTabItemWithIcon("What to sync", theme.ListIcon(), u.what.build()),
		container.NewTabItemWithIcon("Backups", theme.HistoryIcon(), u.buildBackupsTab()),
		container.NewTabItemWithIcon("Settings", theme.SettingsIcon(), u.settings.build()),
	)
	tabs.OnSelected = func(t *container.TabItem) {
		switch t.Text {
		case "Backups":
			u.reloadBackups()
		case "Settings":
			u.settings.load()
		case "What to sync":
			u.what.load()
		}
	}
	return tabs
}

func (u *ui) buildSyncTab() fyne.CanvasObject {
	u.details = widget.NewLabel("")
	u.details.Wrapping = fyne.TextWrapWord
	u.wowLabel = widget.NewLabel("")
	u.warnings = widget.NewLabel("")
	u.warnings.Wrapping = fyne.TextWrapWord
	u.warnings.Importance = widget.WarningImportance
	u.warnings.Hide()
	u.statusCard = widget.NewCard("Checking…", "", container.NewVBox(u.details, u.wowLabel, u.warnings))

	u.pullBtn = widget.NewButtonWithIcon("Pull…", theme.DownloadIcon(), func() { u.startPull(false) })
	u.pushBtn = widget.NewButtonWithIcon("Push", theme.UploadIcon(), func() { u.startPush(false) })
	u.refreshBtn = widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), u.refreshStatus)
	u.progress = widget.NewProgressBarInfinite()
	u.progress.Stop()
	u.progress.Hide()
	u.busyLabel = widget.NewLabel("")
	buttons := container.NewHBox(u.pullBtn, u.pushBtn, u.refreshBtn, u.busyLabel)

	u.logLabel = widget.NewLabel("")
	u.logLabel.Wrapping = fyne.TextWrapWord
	u.logLabel.TextStyle = fyne.TextStyle{Monospace: true}
	u.logScroll = container.NewVScroll(u.logLabel)
	logCard := widget.NewCard("Activity", "", u.logScroll)

	top := container.NewVBox(u.statusCard, buttons, u.progress)
	return container.NewBorder(top, nil, nil, nil, logCard)
}

func (u *ui) onStarted() {
	ver := version.String()
	if m := u.app.Metadata().Version; ver == "dev" && m != "" {
		ver = m // set by fyne package --app-version
	}
	u.logf("WoWSync %s started (%s mode)", ver, u.config().Mode)
	go syncer.WatchWoW(u.ctx, 5*time.Second, u.engine().RunningClients, u.onWoWChange)
	c := u.config()
	if err := c.Validate(); err != nil {
		u.setStatus("Set up WoWSync", "Choose the shared sync folder and your WoW folder in Settings.", nil, nil)
		return
	}
	if c.Mode == config.ModeAuto {
		u.startPull(true)
	} else {
		u.refreshStatus()
	}
}

// onWoWChange runs on the watcher goroutine.
func (u *ui) onWoWChange(running bool) {
	if running {
		u.logf("World of Warcraft started")
	} else {
		u.logf("World of Warcraft closed")
	}
	u.refreshStatus()
	c := u.config()
	if running || c.Mode != config.ModeAuto {
		return
	}
	delay := time.Duration(c.AutoPushDelaySec) * time.Second
	u.logf("Automatic mode: pushing in %s…", delay)
	go func() {
		time.Sleep(delay)
		for i := 0; i < 30; i++ { // wait for a running operation to finish
			if u.startPush(true) {
				return
			}
			time.Sleep(10 * time.Second)
		}
		u.logf("Automatic push skipped: another operation kept running")
	}()
}

// run executes fn in the background with the UI marked busy. It returns
// false if another operation is already running.
func (u *ui) run(label string, fn func(ctx context.Context) error) bool {
	u.busyMu.Lock()
	if u.busy {
		u.busyMu.Unlock()
		return false
	}
	u.busy = true
	u.busyMu.Unlock()
	fyne.Do(func() { u.setBusy(label) })
	go func() {
		err := fn(u.ctx)
		u.busyMu.Lock()
		u.busy = false
		u.busyMu.Unlock()
		fyne.Do(func() {
			u.setBusy("")
			if err != nil {
				u.showError(err)
			}
		})
		u.refreshStatus()
	}()
	return true
}

func (u *ui) setBusy(label string) {
	for _, b := range []*widget.Button{u.pullBtn, u.pushBtn, u.refreshBtn} {
		if label != "" {
			b.Disable()
		} else {
			b.Enable()
		}
	}
	u.busyLabel.SetText(label)
	if label != "" {
		u.progress.Show()
		u.progress.Start()
	} else {
		u.progress.Stop()
		u.progress.Hide()
	}
}

func (u *ui) showError(err error) {
	u.logf("Error: %v", err)
	switch {
	case errors.Is(err, store.ErrNotInitialized):
		dialog.ShowError(fmt.Errorf("%w\n\nCheck the sync folder in Settings.", err), u.win)
	default:
		dialog.ShowError(err, u.win)
	}
}

func (u *ui) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, args...)
	u.logMu.Lock()
	u.logLines = append(u.logLines, line)
	if len(u.logLines) > 400 {
		u.logLines = u.logLines[len(u.logLines)-400:]
	}
	text := strings.Join(u.logLines, "\n")
	u.logMu.Unlock()
	fyne.Do(func() {
		u.logLabel.SetText(text)
		u.logScroll.ScrollToBottom()
	})
}

// notify logs msg and, for automatic operations, shows an OS notification
// (the window is usually hidden then).
func (u *ui) notify(auto bool, title, msg string) {
	u.logf("%s: %s", title, msg)
	if auto {
		u.app.SendNotification(fyne.NewNotification(title, msg))
	}
}

func (u *ui) showWindow() {
	u.win.Show()
	u.win.RequestFocus()
}

// refreshStatus recomputes the status in the background.
func (u *ui) refreshStatus() {
	go func() {
		c := u.config()
		if err := c.Validate(); err != nil {
			u.setStatus("Set up WoWSync", err.Error()+"\nOpen Settings to finish the setup.", nil, nil)
			return
		}
		s, err := u.engine().Status(u.ctx)
		if err != nil {
			title := "Sync folder not available"
			if !errors.Is(err, store.ErrNotInitialized) {
				title = "Could not check status"
			}
			u.setStatus(title, err.Error(), nil, nil)
			return
		}
		u.setStatus(adviceTitle(s.Advice), s.Message, s, s.Warnings)
	}()
}

func adviceTitle(a syncer.Advice) string {
	switch a {
	case syncer.AdviceInSync:
		return "✓ In sync"
	case syncer.AdvicePull:
		return "Pull recommended"
	case syncer.AdvicePush:
		return "Push recommended"
	case syncer.AdviceConflict:
		return "Changed on both sides"
	case syncer.AdviceFirstSync:
		return "First sync on this computer"
	case syncer.AdviceEmptyStore:
		return "The store is empty"
	}
	return string(a)
}

func (u *ui) setStatus(title, msg string, s *syncer.Status, warnings []string) {
	c := u.config()
	var b strings.Builder
	fmt.Fprintf(&b, "Sync folder:  %s\n", orDash(c.StorePath))
	fmt.Fprintf(&b, "WoW folder:   %s  (%s)\n", orDash(c.WoWPath), strings.Join(c.Flavors, ", "))
	if s != nil && s.Latest != nil {
		fmt.Fprintf(&b, "Newest snapshot:  %s from %s\n", s.Latest.Created.Local().Format("Mon 2 Jan 15:04"), s.Latest.Machine)
	}
	if s != nil {
		if s.State.LastSnapshot != "" {
			fmt.Fprintf(&b, "Last sync here:   %s (%s)\n", s.State.LastSync.Local().Format("Mon 2 Jan 15:04"), s.State.LastAction)
		} else {
			b.WriteString("Last sync here:   never\n")
		}
		if !s.Diff.Empty() {
			fmt.Fprintf(&b, "Pull would change:  %s\n", s.Diff.Summary())
		}
	}
	mode := "Manual – nothing happens until you press Push or Pull"
	if c.Mode == config.ModeAuto {
		mode = "Automatic – pushes when WoW closes, pulls when WoWSync starts"
	}
	fmt.Fprintf(&b, "Mode:  %s", mode)
	running, _ := u.engine().RunningClients()

	fyne.Do(func() {
		u.statusCard.SetTitle(title)
		u.statusCard.SetSubTitle(msg)
		u.details.SetText(b.String())
		if len(running) > 0 {
			u.wowLabel.Importance = widget.DangerImportance
			u.wowLabel.SetText("World of Warcraft is running (" + strings.Join(running, ", ") + ") – pull is blocked until it is closed.")
		} else {
			u.wowLabel.Importance = widget.SuccessImportance
			u.wowLabel.SetText("World of Warcraft is not running.")
		}
		if len(warnings) > 0 {
			u.warnings.SetText("⚠ " + strings.Join(limit(warnings, 6), "\n⚠ "))
			u.warnings.Show()
		} else {
			u.warnings.Hide()
		}
		u.pullBtn.Importance = widget.MediumImportance
		u.pushBtn.Importance = widget.MediumImportance
		if s != nil {
			switch s.Advice {
			case syncer.AdvicePull, syncer.AdviceFirstSync:
				u.pullBtn.Importance = widget.HighImportance
			case syncer.AdvicePush, syncer.AdviceEmptyStore:
				u.pushBtn.Importance = widget.HighImportance
			}
		}
		u.pullBtn.Refresh()
		u.pushBtn.Refresh()
	})
}

func limit(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	out := append([]string{}, s[:n]...)
	return append(out, fmt.Sprintf("… and %d more", len(s)-n))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// buildTray installs the system tray icon and menu.
func (u *ui) buildTray(desk desktop.App) {
	u.autoItem = fyne.NewMenuItem("Automatic mode", func() {
		c := u.config()
		if c.Mode == config.ModeAuto {
			c.Mode = config.ModeManual
		} else {
			c.Mode = config.ModeAuto
		}
		if err := u.setConfig(&c); err != nil {
			u.showError(err)
			return
		}
		u.logf("Mode changed to %s", c.Mode)
		u.refreshStatus()
	})
	u.trayMenu = fyne.NewMenu("WoWSync",
		fyne.NewMenuItem("Open WoWSync", u.showWindow),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Pull…", func() { u.showWindow(); u.startPull(false) }),
		fyne.NewMenuItem("Push", func() { u.startPush(false) }),
		fyne.NewMenuItemSeparator(),
		u.autoItem,
	)
	desk.SetSystemTrayMenu(u.trayMenu)
	desk.SetSystemTrayIcon(theme.NewThemedResource(fyne.NewStaticResource("tray.svg", traySVG)))
	desk.SetSystemTrayWindow(u.win)
	u.updateTrayMode()
}

func (u *ui) updateTrayMode() {
	if u.autoItem == nil {
		return
	}
	checked := u.config().Mode == config.ModeAuto
	fyne.Do(func() {
		u.autoItem.Checked = checked
		u.trayMenu.Refresh()
	})
}

func (u *ui) openFolder(dir string) {
	p := filepath.ToSlash(dir)
	if runtime.GOOS == "windows" {
		p = "/" + p
	}
	if err := u.app.OpenURL(&url.URL{Scheme: "file", Path: p}); err != nil {
		u.showError(err)
	}
}
