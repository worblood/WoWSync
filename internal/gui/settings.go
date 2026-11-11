package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/worblood/WoWSync/internal/config"
	"github.com/worblood/WoWSync/internal/store"
	"github.com/worblood/WoWSync/internal/syncer"
	"github.com/worblood/WoWSync/internal/wow"
)

const (
	modeManual = "Manual"
	modeAuto   = "Automatic"
)

type settingsView struct {
	u            *ui
	storePath    *widget.Entry
	storeState   *widget.Label
	initBtn      *widget.Button
	wowPath      *widget.Entry
	flavors      *widget.CheckGroup
	machine      *widget.Entry
	mode         *widget.RadioGroup
	warnMB       *widget.Entry
	maxMB        *widget.Entry
	backupKeep   *widget.Entry
	pushDelay    *widget.Entry
	snapshotKeep *widget.Entry
}

func newSettingsView(u *ui) *settingsView { return &settingsView{u: u} }

func (v *settingsView) build() fyne.CanvasObject {
	v.storePath = widget.NewEntry()
	v.storePath.SetPlaceHolder(`e.g. \\NAS\games\WoWSync or /Volumes/games/WoWSync`)
	v.storePath.OnChanged = func(string) { v.checkStore() }
	v.storeState = widget.NewLabel("")
	v.storeState.Wrapping = fyne.TextWrapWord
	v.initBtn = widget.NewButtonWithIcon("Create store here", theme.FolderNewIcon(), v.initStore)
	v.initBtn.Hide()
	browseStore := widget.NewButtonWithIcon("Browse…", theme.FolderOpenIcon(), func() {
		v.browse(func(p string) { v.storePath.SetText(p) })
	})

	v.wowPath = widget.NewEntry()
	v.wowPath.OnChanged = func(string) { v.detectFlavors() }
	browseWoW := widget.NewButtonWithIcon("Browse…", theme.FolderOpenIcon(), func() {
		v.browse(func(p string) { v.wowPath.SetText(p) })
	})
	v.flavors = widget.NewCheckGroup(nil, nil)
	v.flavors.Horizontal = true

	v.machine = widget.NewEntry()
	v.mode = widget.NewRadioGroup([]string{modeManual, modeAuto}, nil)
	v.mode.Horizontal = true
	modeHelp := helpText("Manual: nothing happens until you press Push or Pull.\n" +
		"Automatic: pushes a little after WoW closes, and checks for new settings when WoWSync starts (you confirm the pull).")

	v.warnMB = widget.NewEntry()
	v.maxMB = widget.NewEntry()
	v.backupKeep = widget.NewEntry()
	v.snapshotKeep = widget.NewEntry()
	v.pushDelay = widget.NewEntry()

	form := widget.NewForm(
		widget.NewFormItem("Sync folder", container.NewBorder(nil, nil, nil, browseStore, v.storePath)),
		widget.NewFormItem("", container.NewVBox(v.storeState, container.NewHBox(v.initBtn))),
		widget.NewFormItem("WoW folder", container.NewBorder(nil, nil, nil, browseWoW, v.wowPath)),
		widget.NewFormItem("Game versions", v.flavors),
		widget.NewFormItem("Computer name", v.machine),
		widget.NewFormItem("Mode", container.NewVBox(v.mode, modeHelp)),
		widget.NewFormItem("Warn above (MB)", v.warnMB),
		widget.NewFormItem("Skip files above (MB)", v.maxMB),
		widget.NewFormItem("Push delay (seconds)", v.pushDelay),
		widget.NewFormItem("Backups to keep", v.backupKeep),
		widget.NewFormItem("Snapshots to keep", v.snapshotKeep),
	)
	form.Items[7].HintText = "0 = no limit"
	form.Items[8].HintText = "automatic mode: wait after WoW closes"
	form.Items[10].HintText = "used by Prune"

	save := widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), v.save)
	save.Importance = widget.HighImportance
	prune := widget.NewButtonWithIcon("Prune store…", theme.DeleteIcon(), v.prune)
	openCfg := widget.NewButtonWithIcon("Open config folder", theme.FolderOpenIcon(), func() {
		v.u.openFolder(dirOf(v.u.paths.ConfigFile))
	})
	v.load()
	return container.NewBorder(nil, container.NewHBox(save, prune, openCfg), nil, nil, container.NewVScroll(form))
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i > 0 {
		return p[:i]
	}
	return p
}

func (v *settingsView) load() {
	c := v.u.config()
	v.storePath.SetText(c.StorePath)
	v.wowPath.SetText(c.WoWPath)
	v.detectFlavors()
	v.flavors.SetSelected(c.Flavors)
	v.machine.SetText(c.MachineName)
	if c.Mode == config.ModeAuto {
		v.mode.SetSelected(modeAuto)
	} else {
		v.mode.SetSelected(modeManual)
	}
	v.warnMB.SetText(strconv.Itoa(c.WarnFileSizeMB))
	v.maxMB.SetText(strconv.Itoa(c.MaxFileSizeMB))
	v.backupKeep.SetText(strconv.Itoa(c.BackupKeep))
	v.snapshotKeep.SetText(strconv.Itoa(c.SnapshotKeep))
	v.pushDelay.SetText(strconv.Itoa(c.AutoPushDelaySec))
	v.checkStore()
}

func (v *settingsView) browse(set func(string)) {
	d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			return
		}
		set(uri.Path())
	}, v.u.win)
	d.Resize(fyne.NewSize(760, 520))
	d.Show()
}

func (v *settingsView) detectFlavors() {
	root, picked := wow.SplitInstallPath(strings.TrimSpace(v.wowPath.Text))
	found, _ := wow.DetectFlavors(root)
	selected := append([]string{}, v.flavors.Selected...)
	options := append([]string{}, found...)
	for _, f := range append(v.u.config().Flavors, picked) {
		if f != "" && !containsStr(options, f) {
			options = append(options, f)
		}
	}
	if picked != "" && !containsStr(selected, picked) {
		selected = append(selected, picked)
	}
	v.flavors.Options = options
	v.flavors.SetSelected(selected)
	v.flavors.Refresh()
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (v *settingsView) checkStore() {
	p := strings.TrimSpace(v.storePath.Text)
	v.initBtn.Hide()
	switch _, err := store.OpenFolder(p); {
	case p == "":
		v.storeState.Importance = widget.WarningImportance
		v.storeState.SetText("Choose a folder on your NAS (or any shared/cloud folder) that every computer can reach.")
	case err == nil:
		v.storeState.Importance = widget.SuccessImportance
		v.storeState.SetText("✓ WoWSync store found.")
	case errors.Is(err, store.ErrNotInitialized) && dirExists(p):
		v.storeState.Importance = widget.WarningImportance
		v.storeState.SetText("No WoWSync store in this folder yet. Create one (only once, on the first computer).")
		v.initBtn.Show()
	default:
		v.storeState.Importance = widget.DangerImportance
		v.storeState.SetText("Not reachable – is the NAS share connected/mounted? " + err.Error())
	}
	v.storeState.Refresh()
}

func (v *settingsView) initStore() {
	p := strings.TrimSpace(v.storePath.Text)
	if _, err := store.InitFolder(p); err != nil {
		v.u.showError(err)
		return
	}
	v.u.logf("Created store in %s", p)
	v.checkStore()
}

func (v *settingsView) collect() (*config.Config, error) {
	c := v.u.config()
	c.StorePath = strings.TrimSpace(v.storePath.Text)
	c.WoWPath = strings.TrimSpace(v.wowPath.Text)
	if root, _ := wow.SplitInstallPath(c.WoWPath); root != "" {
		c.WoWPath = root
	}
	c.Flavors = append([]string{}, v.flavors.Selected...)
	if len(c.Flavors) == 0 {
		return nil, errors.New("select at least one game version")
	}
	c.MachineName = strings.TrimSpace(v.machine.Text)
	c.Mode = config.ModeManual
	if v.mode.Selected == modeAuto {
		c.Mode = config.ModeAuto
	}
	for _, f := range []struct {
		e    *widget.Entry
		dst  *int
		name string
	}{
		{v.warnMB, &c.WarnFileSizeMB, "Warn above"},
		{v.maxMB, &c.MaxFileSizeMB, "Skip files above"},
		{v.backupKeep, &c.BackupKeep, "Backups to keep"},
		{v.snapshotKeep, &c.SnapshotKeep, "Snapshots to keep"},
		{v.pushDelay, &c.AutoPushDelaySec, "Push delay"},
	} {
		n, err := strconv.Atoi(strings.TrimSpace(f.e.Text))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s: enter a whole number", f.name)
		}
		*f.dst = n
	}
	return &c, nil
}

func (v *settingsView) save() {
	c, err := v.collect()
	if err != nil {
		v.u.showError(err)
		return
	}
	if err := v.u.setConfig(c); err != nil {
		v.u.showError(err)
		return
	}
	v.u.logf("Settings saved")
	v.load()
	v.u.refreshStatus()
	if err := c.Validate(); err != nil {
		dialog.ShowInformation("Saved", "Saved, but WoWSync is not ready yet:\n"+err.Error(), v.u.win)
	}
}

func (v *settingsView) prune() {
	c := v.u.config()
	dialog.ShowConfirm("Prune store",
		fmt.Sprintf("Delete all but the newest %d snapshots from the store, and the data only they use?\nThis affects every computer.", c.SnapshotKeep),
		func(ok bool) {
			if !ok {
				return
			}
			v.u.run("Pruning…", func(ctx context.Context) error {
				st, err := v.u.engine().OpenStore()
				if err != nil {
					return err
				}
				res, err := st.Prune(ctx, c.SnapshotKeep, 24*time.Hour)
				if err != nil {
					return err
				}
				v.u.logf("Pruned %d snapshot(s), %d unused file(s), freed %s", res.SnapshotsRemoved, res.ObjectsRemoved, humanSize(res.BytesFreed))
				return nil
			})
		}, v.u.win)
}

// whatView edits the sync categories, local CVars and extra excludes.
type whatView struct {
	u          *ui
	checks     map[string]*widget.Check
	localCVars *widget.Entry
	excludes   *widget.Entry
}

func newWhatView(u *ui) *whatView { return &whatView{u: u, checks: map[string]*widget.Check{}} }

func (v *whatView) build() fyne.CanvasObject {
	cats := container.NewVBox()
	for _, c := range wow.Categories {
		chk := widget.NewCheck(c.Name, nil)
		v.checks[c.ID] = chk
		cats.Add(container.NewVBox(chk, indent(helpText(c.Description))))
	}
	v.localCVars = widget.NewMultiLineEntry()
	v.localCVars.SetMinRowsVisible(8)
	v.localCVars.Wrapping = fyne.TextWrapOff
	reset := widget.NewButtonWithIcon("Reset to defaults", theme.MediaReplayIcon(), func() {
		v.localCVars.SetText(strings.Join(wow.DefaultLocalCVars, "\n"))
	})
	cvarHelp := helpText("Config.wtf is merged setting by setting. CVars matching these patterns (one per line, * = any text) " +
		"stay as they are on this computer – graphics, window, sound device, UI scale …")

	v.excludes = widget.NewMultiLineEntry()
	v.excludes.SetMinRowsVisible(4)
	v.excludes.SetPlaceHolder("Interface/AddOns/MyDevAddon/\n*.log")
	exHelp := helpText("Extra files or folders to skip, relative to _retail_ (one per line). Always skipped: " +
		strings.Join(wow.DefaultExcludes, "  "))

	body := container.NewVBox(
		widget.NewCard("Categories", "Choose what this computer syncs. Unchecked items are left alone, both when pushing and pulling.", cats),
		widget.NewCard("Settings that stay on this computer", "", container.NewVBox(cvarHelp, v.localCVars, container.NewHBox(reset))),
		widget.NewCard("Extra exclusions", "", container.NewVBox(exHelp, v.excludes)),
	)
	save := widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), v.save)
	save.Importance = widget.HighImportance
	v.load()
	return container.NewBorder(nil, container.NewHBox(save), nil, nil, container.NewVScroll(body))
}

func (v *whatView) load() {
	c := v.u.config()
	for id, chk := range v.checks {
		chk.SetChecked(c.Categories[id])
	}
	v.localCVars.SetText(strings.Join(c.EffectiveLocalCVars(), "\n"))
	v.excludes.SetText(strings.Join(c.ExtraExcludes, "\n"))
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (v *whatView) save() {
	c := v.u.config()
	cats := map[string]bool{}
	for id, chk := range v.checks {
		cats[id] = chk.Checked
	}
	c.Categories = cats
	c.LocalCVars = lines(v.localCVars.Text)
	if strings.Join(c.LocalCVars, "\n") == strings.Join(wow.DefaultLocalCVars, "\n") {
		c.LocalCVars = nil // follow future default updates
	}
	c.ExtraExcludes = lines(v.excludes.Text)
	if err := v.u.setConfig(&c); err != nil {
		v.u.showError(err)
		return
	}
	v.u.logf("Sync selection saved")
	v.u.refreshStatus()
}

// buildBackupsTab lists local backups with restore.
func (u *ui) buildBackupsTab() fyne.CanvasObject {
	u.backupList = widget.NewList(
		func() int { return len(u.backups) },
		func() fyne.CanvasObject {
			return container.NewBorder(nil, nil, widget.NewIcon(theme.HistoryIcon()), widget.NewLabel(""), widget.NewLabel(""))
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			b := u.backups[id]
			c := o.(*fyne.Container)
			reason := map[string]string{"pre-pull": "before pull", "pre-restore": "before restore"}[b.Reason]
			if reason == "" {
				reason = b.Reason
			}
			c.Objects[0].(*widget.Label).SetText(fmt.Sprintf("%s  –  %s", b.Created.Local().Format("Mon 2 Jan 2006 15:04:05"), reason))
			c.Objects[2].(*widget.Label).SetText(fmt.Sprintf("%d files · %s", b.Files, humanSize(b.Size)))
		},
	)
	u.backupList.OnSelected = func(id widget.ListItemID) {
		u.backupSel = id
		u.restoreBtn.Enable()
	}
	u.backupList.OnUnselected = func(widget.ListItemID) {
		u.backupSel = -1
		u.restoreBtn.Disable()
	}
	u.restoreBtn = widget.NewButtonWithIcon("Restore…", theme.MediaReplayIcon(), u.restoreSelected)
	u.restoreBtn.Importance = widget.HighImportance
	u.restoreBtn.Disable()
	open := widget.NewButtonWithIcon("Open backup folder", theme.FolderOpenIcon(), func() { u.openFolder(u.paths.BackupDir) })
	help := helpText("A backup of the whole WTF folder (plus any addon file that changes) is made before every pull. " +
		"Restoring puts those files back and removes files the pull added. The current state is backed up first, so a restore can be undone.")
	u.backupsEmpty = widget.NewLabel("No backups yet.")
	return container.NewBorder(container.NewVBox(help, u.backupsEmpty), container.NewHBox(u.restoreBtn, open), nil, nil, u.backupList)
}

func (u *ui) reloadBackups() {
	list, err := u.engine().ListBackups()
	if err != nil {
		u.logf("Could not list backups: %v", err)
	}
	u.backups = list
	u.backupSel = -1
	u.backupList.UnselectAll()
	u.restoreBtn.Disable()
	if len(list) == 0 {
		u.backupsEmpty.Show()
	} else {
		u.backupsEmpty.Hide()
	}
	u.backupList.Refresh()
}

func (u *ui) restoreSelected() {
	if u.backupSel < 0 || u.backupSel >= len(u.backups) {
		return
	}
	b := u.backups[u.backupSel]
	dialog.ShowConfirm("Restore backup",
		fmt.Sprintf("Restore the backup from %s (%d files)?\n\nWoW must be closed. The current files are backed up first.",
			b.Created.Local().Format("Mon 2 Jan 15:04"), b.Files),
		func(ok bool) {
			if !ok {
				return
			}
			u.run("Restoring…", func(ctx context.Context) error {
				res, err := u.engine().Restore(ctx, b.ID, syncer.RestoreOptions{})
				if err != nil {
					return err
				}
				u.logf("Restored %d files, removed %d. Undo: restore backup %s", res.Restored, res.Removed, res.Safety.ID)
				fyne.Do(u.reloadBackups)
				return nil
			})
		}, u.win)
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// helpText is a wrapped, italic explanation (normal contrast for readability).
func helpText(s string) *widget.Label {
	l := widget.NewLabel(s)
	l.Wrapping = fyne.TextWrapWord
	l.TextStyle = fyne.TextStyle{Italic: true}
	return l
}

// indent shifts o right so it lines up with a check box label.
func indent(o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewBorder(nil, nil, sized(layout.NewSpacer(), 22, 0), nil, o)
}
