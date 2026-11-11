// Command wowsync-gui is the WoWSync desktop app (window + tray icon).
//
// It also accepts the CLI commands, e.g.
// "WoWSync.app/Contents/MacOS/wowsync-gui status".
package main

import (
	_ "embed"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/worblood/WoWSync/internal/cli"
	"github.com/worblood/WoWSync/internal/gui"
)

//go:embed Icon.png
var icon []byte

func main() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit((&cli.App{}).Run(os.Args[1:]))
	}
	fs := flag.NewFlagSet("wowsync-gui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hidden := fs.Bool("hidden", false, "start in the system tray")
	_ = fs.Parse(os.Args[1:]) // ignore unknown flags such as macOS -psn_…
	gui.Run(gui.Options{Icon: icon, Hidden: *hidden})
}
