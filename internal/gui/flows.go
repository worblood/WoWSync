package gui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/worblood/WoWSync/internal/syncer"
	"github.com/worblood/WoWSync/internal/wow"
)

// startPull plans a pull and shows the preview. In automatic mode
// (auto=true) nothing is shown when there is nothing to pull.
func (u *ui) startPull(auto bool) bool {
	return u.run("Checking the store…", func(ctx context.Context) error {
		e := u.engine()
		plan, err := e.PlanPull(ctx)
		if err != nil {
			if auto {
				u.notify(true, "WoWSync", "Automatic pull failed: "+err.Error())
				return nil
			}
			return err
		}
		if plan.Snapshot == nil {
			if !auto {
				fyne.Do(func() {
					dialog.ShowInformation("Nothing to pull", "The store is empty. Push from the computer with the settings you want to share.", u.win)
				})
			}
			return nil
		}
		if plan.Diff.Empty() {
			if _, err := e.Pull(ctx, plan, syncer.PullOptions{}); err != nil && !errors.Is(err, syncer.ErrWoWRunning) {
				return err
			}
			u.logf("Already up to date with the newest snapshot from %s", plan.Snapshot.Machine)
			if !auto {
				fyne.Do(func() { dialog.ShowInformation("Up to date", "This computer already has the newest settings.", u.win) })
			}
			return nil
		}
		u.logf("Pull preview: %s", plan.Diff.Summary())
		fyne.Do(func() {
			u.showWindow()
			u.showPullPreview(plan, auto)
		})
		return nil
	})
}

// previewRow is one line in the preview list.
type previewRow struct {
	header bool
	mark   string
	text   string
	detail string
}

func previewRows(d syncer.Diff) []previewRow {
	var rows []previewRow
	marks := map[wow.ChangeKind]string{wow.Added: "+", wow.Modified: "~", wow.Removed: "−"}
	for _, g := range d.ByCategory() {
		rows = append(rows, previewRow{header: true, text: fmt.Sprintf("%s (%d)", g.Category.Name, len(g.Changes))})
		for _, c := range g.Changes {
			size := c.NewSize
			if c.Kind == wow.Removed {
				size = c.OldSize
			}
			rows = append(rows, previewRow{mark: marks[c.Kind], text: c.Path, detail: humanSize(size)})
		}
	}
	for _, cv := range d.CVars {
		rows = append(rows, previewRow{header: true, text: fmt.Sprintf("Config.wtf – %s (%d settings)", cv.Flavor, len(cv.Changes))})
		for _, c := range cv.Changes {
			r := previewRow{mark: marks[c.Kind], text: c.Name}
			switch c.Kind {
			case wow.Added:
				r.detail = fmt.Sprintf("= %q", c.New)
			case wow.Modified:
				r.detail = fmt.Sprintf("%q → %q", c.Old, c.New)
			case wow.Removed:
				r.detail = fmt.Sprintf("(was %q)", c.Old)
			}
			rows = append(rows, r)
		}
	}
	return rows
}

func changeList(rows []previewRow) *widget.List {
	return widget.NewList(
		func() int { return len(rows) },
		func() fyne.CanvasObject {
			mark := widget.NewLabel("~")
			mark.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
			text := widget.NewLabel("")
			text.Truncation = fyne.TextTruncateEllipsis
			detail := widget.NewLabel("")
			return container.NewBorder(nil, nil, mark, detail, text)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			r := rows[id]
			c := o.(*fyne.Container)
			text := c.Objects[0].(*widget.Label)
			mark := c.Objects[1].(*widget.Label)
			detail := c.Objects[2].(*widget.Label)
			text.SetText(r.text)
			text.TextStyle = fyne.TextStyle{Bold: r.header}
			mark.SetText(r.mark)
			switch r.mark {
			case "+":
				mark.Importance = widget.SuccessImportance
			case "−":
				mark.Importance = widget.DangerImportance
			default:
				mark.Importance = widget.WarningImportance
			}
			if r.header {
				mark.SetText(" ")
			}
			mark.Refresh()
			text.Refresh()
			detail.SetText(r.detail)
		},
	)
}

func sized(o fyne.CanvasObject, w, h float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(w, h))
	return container.NewStack(r, o)
}

func (u *ui) showPullPreview(plan *syncer.PullPlan, auto bool) {
	snap := plan.Snapshot
	head := widget.NewLabel(fmt.Sprintf("Newest snapshot from %s, %s.\nThis will change: %s.\nThe WTF folder is backed up first.",
		snap.Machine, snap.Created.Local().Format("Mon 2 Jan 15:04"), plan.Diff.Summary()))
	head.Wrapping = fyne.TextWrapWord

	top := container.NewVBox(head)
	if len(plan.Warnings) > 0 {
		w := widget.NewLabel("⚠ " + strings.Join(limit(plan.Warnings, 5), "\n⚠ "))
		w.Wrapping = fyne.TextWrapWord
		w.Importance = widget.WarningImportance
		top.Add(w)
	}
	content := container.NewBorder(top, nil, nil, nil, sized(changeList(previewRows(plan.Diff)), 760, 340))

	title := "Pull preview"
	if auto {
		title = "New settings available"
	}
	d := dialog.NewCustomWithoutButtons(title, content, u.win)
	cancel := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() {
		d.Hide()
		u.logf("Pull cancelled – nothing was changed")
	})
	apply := widget.NewButtonWithIcon("Back up and pull", theme.DownloadIcon(), func() {
		d.Hide()
		u.applyPull(plan, auto)
	})
	apply.Importance = widget.HighImportance
	buttons := []fyne.CanvasObject{cancel}
	if len(plan.Running) > 0 {
		apply.Disable()
		apply.SetText("Close WoW to pull")
		buttons = append(buttons, widget.NewButtonWithIcon("Check again", theme.ViewRefreshIcon(), func() {
			d.Hide()
			u.startPull(false)
		}))
	}
	buttons = append(buttons, apply)
	d.SetButtons(buttons)
	d.Resize(fyne.NewSize(820, 560))
	d.Show()
}

func (u *ui) applyPull(plan *syncer.PullPlan, auto bool) {
	u.run("Pulling…", func(ctx context.Context) error {
		res, err := u.engine().Pull(ctx, plan, syncer.PullOptions{})
		if errors.Is(err, syncer.ErrStoreChanged) {
			u.logf("The store changed during the preview – checking again")
			u.afterIdle(func() bool { return u.startPull(auto) })
			return nil
		}
		if err != nil {
			return err
		}
		u.notify(auto, "Pull complete", fmt.Sprintf("%d files written, %d removed, %d Config.wtf settings. Backup: %s",
			res.Written, res.Removed, res.CVars, res.Backup.ID))
		fyne.Do(u.reloadBackups)
		return nil
	})
}

// startPush plans and runs a push. In automatic mode conflicts are only
// reported, never resolved.
func (u *ui) startPush(auto bool) bool {
	return u.run("Scanning files…", func(ctx context.Context) error {
		e := u.engine()
		plan, err := e.PlanPush(ctx)
		if err != nil {
			if auto {
				u.notify(true, "WoWSync", "Automatic push failed: "+err.Error())
				return nil
			}
			return err
		}
		if plan.Latest != nil && plan.Diff.Empty() {
			if _, err := e.Push(ctx, plan, syncer.PushOptions{}); err != nil {
				return err
			}
			u.logf("Nothing to push – the store already has this computer's settings")
			if !auto {
				fyne.Do(func() {
					dialog.ShowInformation("Nothing to push", "The store already has this computer's settings.", u.win)
				})
			}
			return nil
		}
		if plan.Conflict {
			if auto {
				u.notify(true, "Automatic push skipped",
					fmt.Sprintf("The store has newer settings from %s. Open WoWSync to pull or push.", plan.Latest.Machine))
				return nil
			}
			fyne.Do(func() { u.showConflict(plan) })
			return nil
		}
		if plan.WoWRunning && !auto {
			fyne.Do(func() {
				dialog.ShowConfirm("World of Warcraft is running",
					"WoW saves addon settings when you log out, exit or /reload, so the newest changes may be missing.\n\nPush anyway?",
					func(ok bool) {
						if ok {
							u.applyPush(plan, false, false)
						}
					}, u.win)
			})
			return nil
		}
		return u.doPush(ctx, e, plan, false, auto)
	})
}

func (u *ui) doPush(ctx context.Context, e *syncer.Engine, plan *syncer.PushPlan, force, auto bool) error {
	u.logf("Pushing: %s", plan.Diff.Summary())
	res, err := e.Push(ctx, plan, syncer.PushOptions{Force: force})
	if errors.Is(err, syncer.ErrStoreChanged) && !auto {
		fyne.Do(func() {
			dialog.ShowInformation("Store changed", "Another computer pushed in the meantime. Check the status and try again.", u.win)
		})
		return nil
	}
	if err != nil {
		if auto {
			u.notify(true, "Automatic push failed", err.Error())
			return nil
		}
		return err
	}
	u.notify(auto, "Push complete", fmt.Sprintf("%s (%d new files, %s)", plan.Diff.Summary(), res.Uploaded, humanSize(res.UploadedBytes)))
	return nil
}

func (u *ui) applyPush(plan *syncer.PushPlan, force, auto bool) {
	u.run("Pushing…", func(ctx context.Context) error {
		return u.doPush(ctx, u.engine(), plan, force, auto)
	})
}

func (u *ui) showConflict(plan *syncer.PushPlan) {
	msg := widget.NewLabel(fmt.Sprintf(
		"The store has newer settings from %s (%s) that this computer has not pulled.\n\n"+
			"• Pull first to get them (your files are backed up), or\n"+
			"• Push anyway to replace them with this computer's settings. The other snapshot stays in the store's history.",
		plan.Latest.Machine, plan.Latest.Created.Local().Format("Mon 2 Jan 15:04")))
	msg.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustomWithoutButtons("Changed on another computer", sized(msg, 520, 140), u.win)
	cancel := widget.NewButton("Cancel", func() { d.Hide() })
	pull := widget.NewButtonWithIcon("Pull first", theme.DownloadIcon(), func() {
		d.Hide()
		u.startPull(false)
	})
	pull.Importance = widget.HighImportance
	push := widget.NewButtonWithIcon("Push anyway", theme.UploadIcon(), func() {
		d.Hide()
		u.applyPush(plan, true, false)
	})
	push.Importance = widget.DangerImportance
	d.SetButtons([]fyne.CanvasObject{cancel, push, pull})
	d.Show()
}

// afterIdle retries start (which returns false while busy) for a while.
func (u *ui) afterIdle(start func() bool) {
	go func() {
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if start() {
				return
			}
		}
	}()
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
