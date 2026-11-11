# WoWSync

Desktop app (Windows + macOS) that keeps World of Warcraft settings, SavedVariables
and AddOns in sync between several computers.

Snapshots are stored in a **shared folder** – typically a NAS share
(`\\NAS\wow` on Windows, `/Volumes/wow` on macOS). Nothing is stored in this
GitHub repository except the source code.

## Features

- **Manual mode** (default): nothing happens until you press *Push* or *Pull*.
- **Automatic mode**: the tray icon notices when WoW closes and pushes; WoWSync
  pulls on start-up (with preview and confirmation).
- **Pull preview**: added / modified / removed files and changed CVars are listed
  and must be confirmed before anything is written.
- **Pull is blocked while WoW is running.**
- **Backup before every pull** (zip of the whole `WTF` folder plus any other file
  that will be overwritten or deleted), with one-click restore.
- **Choose what to sync** per category: AddOns, addon settings (SavedVariables),
  key bindings, macros, chat, account/character settings, other WTF files and
  `Config.wtf`.
- **`Config.wtf` is merged per CVar.** Machine-specific CVars (graphics, window,
  sound device, UI scale …) stay local; the list is editable.
- Supports `_retail_`, `_classic_`, `_classic_era_`, `_ptr_`, … side by side.
- Excludes `Cache/`, `Logs/`, `Errors/`, `Screenshots/`, `*.bak`, `.DS_Store`, …
- Line endings in text files are normalised (CRLF → LF) and paths are compared
  case-insensitively and Unicode-normalised, so Windows ↔ macOS does not produce
  phantom changes.
- Large files trigger a warning (and an optional hard limit).
- Content-addressed store: identical files are stored once, files are written once
  and never modified, so a NAS or cloud folder never sees write conflicts.

## Command line

```
wowsync status                 show local vs. store
wowsync pull  [-y] [--force]   preview, back up and apply the latest snapshot
wowsync push  [--force]        upload the local state as a new snapshot
wowsync backups                list local backups
wowsync restore [ID|latest]    restore a backup
wowsync snapshots              list snapshots in the store
wowsync prune [--keep N]       delete old snapshots and unused data
wowsync init-store PATH        create a new store in an (empty) folder
wowsync config show|path|set KEY VALUE|categories
```

## First-time setup

1. Create a folder on the NAS, e.g. `\\NAS\games\WoWSync`, and make sure every
   computer can open it (mount it on macOS, map or use the UNC path on Windows).
2. Start WoWSync → *Settings*: choose the sync folder (press *Initialize* the first
   time), check the WoW folder and machine name, and save.
3. On the computer with the "best" settings: **Push**. On the others: **Pull**.

The configuration lives in the user's config folder and is never committed:

| OS      | Config                                        | Backups / cache                          |
|---------|-----------------------------------------------|------------------------------------------|
| macOS   | `~/Library/Application Support/WoWSync/`      | `~/Library/Application Support/WoWSync/` |
| Windows | `%AppData%\WoWSync\`                          | `%LocalAppData%\WoWSync\`                |

## Download / build

Releases are built by GitHub Actions for Windows (x64), macOS Apple Silicon and
macOS Intel when a tag `v*` is pushed. See *Releases*.

Build locally (GUI needs cgo and a C compiler):

```bash
go test ./...
go build ./cmd/wowsync          # CLI, pure Go
go build ./cmd/wowsync-gui      # GUI (Fyne)
```

### Unsigned builds

The apps are not code-signed, which is fine for personal use:

- **Windows SmartScreen** may say "Windows protected your PC". Click
  *More info* → *Run anyway*.
- **macOS Gatekeeper** may say the app "cannot be opened" or "is damaged".
  Right-click the app → *Open*, or run
  `xattr -dr com.apple.quarantine /Applications/WoWSync.app` once.

## Design

See [docs/DESIGN.md](docs/DESIGN.md) (Norwegian) for architecture, store format and UI design.
