// Package version holds the application version, set at build time with
// -ldflags "-X github.com/worblood/WoWSync/internal/version.Version=v1.2.3".
package version

import "runtime/debug"

// Version is overridden at link time for release builds.
var Version = "dev"

// String returns the best known version string.
func String() string {
	if Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return Version
}
