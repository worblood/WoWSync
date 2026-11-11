package wow

import (
	"path/filepath"
	"strings"
)

// Windows executable names of the game clients (retail, PTR, beta, classic).
var windowsExeNames = map[string]bool{
	"wow.exe": true, "wowt.exe": true, "wowb.exe": true, "wow-64.exe": true,
	"wowclassic.exe": true, "wowclassict.exe": true, "wowclassicb.exe": true,
	"wowt-64.exe": true, "wowb-64.exe": true,
}

// IsWoWProcess reports whether a process name belongs to a WoW game client.
// It accepts a bare name or a full path. Launchers and addon managers such
// as WowUp are not matched.
func IsWoWProcess(name string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if windowsExeNames[base] {
		return true
	}
	// macOS: "World of Warcraft", "World of Warcraft Classic", … (p_comm is
	// truncated to 16 characters, hence the short prefix).
	return strings.HasPrefix(base, "world of warcraf") && !strings.Contains(base, "launcher")
}

// RunningClients returns the names of running WoW game processes.
func RunningClients() ([]string, error) {
	names, err := listProcesses()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if IsWoWProcess(n) {
			out = append(out, filepath.Base(n))
		}
	}
	return out, nil
}

// IsRunning reports whether any WoW client is running.
func IsRunning() (bool, error) {
	c, err := RunningClients()
	return len(c) > 0, err
}
