// Command wowsync is the command line version of WoWSync.
package main

import (
	"os"

	"github.com/worblood/WoWSync/internal/cli"
)

func main() {
	os.Exit((&cli.App{}).Run(os.Args[1:]))
}
