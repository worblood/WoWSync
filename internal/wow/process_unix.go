//go:build !windows

package wow

import (
	"os/exec"
	"strings"
)

// listProcesses uses ps(1), which exists on macOS and Linux and needs no cgo.
func listProcesses() ([]string, error) {
	out, err := exec.Command("ps", "-A", "-o", "comm=").Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}
