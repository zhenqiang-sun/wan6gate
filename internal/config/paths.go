package config

import (
	"os"
	"path/filepath"
)

const (
	defaultBaseDir = "/data/router-ops/wan6gate"
	envBaseDir     = "WAN6GATE_DIR"

	BinaryDir  = "/data/router-ops/bin"
	BinaryPath = "/data/router-ops/bin/wan6gate"

	CronFile     = "/etc/crontabs/root"
	WatchdogPath = "/etc/crontabs/patches/wan6gate_watchdog.sh"
)

func BaseDir() string {
	if v := os.Getenv(envBaseDir); v != "" {
		return v
	}
	return defaultBaseDir
}

func ConfigPath() string { return filepath.Join(BaseDir(), "config.json") }
func LogDir() string     { return filepath.Join(BaseDir(), "logs") }
func LogPath() string    { return filepath.Join(LogDir(), "wan6gate.log") }
func PIDPath() string    { return filepath.Join(BaseDir(), "wan6gate.pid") }
func LockPath() string   { return filepath.Join(BaseDir(), "wan6gate.lock") }
