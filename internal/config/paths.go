package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
)

// Paths are the per-machine files and folders WoWSync uses.
type Paths struct {
	ConfigFile string // settings
	StateFile  string // last synced snapshot etc.
	DataDir    string // backups and caches
	BackupDir  string
	HashCache  string
}

// DefaultPaths returns the standard locations for this OS. Setting
// WOWSYNC_HOME puts everything in that one folder (portable / tests).
func DefaultPaths() (Paths, error) {
	if home := os.Getenv("WOWSYNC_HOME"); home != "" {
		return PathsIn(home, home), nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	data, err := dataDir()
	if err != nil {
		return Paths{}, err
	}
	return PathsIn(filepath.Join(cfg, "WoWSync"), filepath.Join(data, "WoWSync")), nil
}

// PathsIn builds Paths from a config folder and a data folder.
func PathsIn(configDir, dataDir string) Paths {
	return Paths{
		ConfigFile: filepath.Join(configDir, "config.json"),
		StateFile:  filepath.Join(configDir, "state.json"),
		DataDir:    dataDir,
		BackupDir:  filepath.Join(dataDir, "backups"),
		HashCache:  filepath.Join(dataDir, "hashcache.json.gz"),
	}
}

func dataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return d, nil
		}
		return os.UserCacheDir()
	case "darwin":
		return os.UserConfigDir() // ~/Library/Application Support
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return d, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	}
}

// WriteFileAtomic writes data to path via a temp file and rename, creating
// the parent folder if needed.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := path + ".tmp-" + hex.EncodeToString(rnd[:])
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
