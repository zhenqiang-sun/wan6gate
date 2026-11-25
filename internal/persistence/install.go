package persistence

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"wan6gate/internal/config"
	"wan6gate/internal/firewall"
)

const watchdogScript = `#!/bin/sh
# wan6gate watchdog (managed by 'wan6gate install') — do not edit
LOCK=/tmp/wan6gate-watchdog.lock
exec 9>"$LOCK" 2>/dev/null
flock -n 9 2>/dev/null || exit 0

BIN=/data/router-ops/bin/wan6gate
PIDFILE=/data/router-ops/wan6gate/wan6gate.pid
LOG=/data/router-ops/wan6gate/logs/wan6gate.log

[ -x "$BIN" ] || exit 0

# Cap log file size in case of external runaway (> 5MB)
if [ -f "$LOG" ] && [ $(wc -c < "$LOG" 2>/dev/null || echo 0) -gt 5242880 ]; then
    tail -n 1000 "$LOG" > "${LOG}.tmp" 2>/dev/null && mv "${LOG}.tmp" "$LOG" 2>/dev/null
fi

# Already running?
if [ -f "$PIDFILE" ]; then
    pid="$(cat "$PIDFILE" 2>/dev/null)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        # check it's actually wan6gate
        if grep -q wan6gate "/proc/$pid/cmdline" 2>/dev/null; then
            exit 0
        fi
    fi
fi

# Also check by process name
if pgrep -f 'wan6gate daemon' >/dev/null 2>&1; then
    pgrep -f 'wan6gate daemon' | head -1 > "$PIDFILE"
    exit 0
fi

mkdir -p "$(dirname "$LOG")"
# Detach via subshell + redirected stdio (busybox lacks nohup; cron has no tty
# so SIGHUP is not an issue; init reparents orphans).
("$BIN" daemon </dev/null >>"$LOG" 2>&1 &
 echo $! > "$PIDFILE")
`

const cronLine = "*/1 * * * * /etc/crontabs/patches/wan6gate_watchdog.sh >/dev/null 2>&1"

// Install ensures the binary, dirs, watchdog and cron line are in place.
// Idempotent — safe to re-run.
func Install() error {
	// 1. Ensure /data/router-ops/{bin,wan6gate/logs}
	if err := os.MkdirAll(config.BinaryDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", config.BinaryDir, err)
	}
	if err := os.MkdirAll(config.BaseDir(), 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", config.BaseDir(), err)
	}
	if err := os.MkdirAll(config.LogDir(), 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", config.LogDir(), err)
	}

	// 2. Copy current executable to BinaryPath if not already there
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	abs, _ := filepath.EvalSymlinks(self)
	if abs == "" {
		abs = self
	}
	if abs != config.BinaryPath {
		if err := copyFile(abs, config.BinaryPath, 0755); err != nil {
			return fmt.Errorf("copy binary: %w", err)
		}
		fmt.Fprintf(os.Stderr, "binary copied to %s\n", config.BinaryPath)
	}

	// 3. Write watchdog
	if err := os.MkdirAll(filepath.Dir(config.WatchdogPath), 0755); err != nil {
		return fmt.Errorf("mkdir patches dir: %w", err)
	}
	if err := atomicWrite(config.WatchdogPath, []byte(watchdogScript), 0755); err != nil {
		return fmt.Errorf("write watchdog: %w", err)
	}
	fmt.Fprintf(os.Stderr, "watchdog at %s\n", config.WatchdogPath)

	// 4. Add cron line if absent
	if err := ensureCronLine(); err != nil {
		return fmt.Errorf("ensure cron line: %w", err)
	}

	// 5. Restart cron (best-effort)
	if out, err := exec.Command("/etc/init.d/cron", "restart").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cron restart: %s: %v\n", strings.TrimSpace(string(out)), err)
	} else {
		fmt.Fprintln(os.Stderr, "cron restarted")
	}

	// 6. Install fw3 wan→18080 DROP belt-and-suspenders rule.
	// (safe to call repeatedly; the AddUILockdownRule helper deletes-then-recreates).
	if err := firewall.AddUILockdownRule(18080); err != nil {
		fmt.Fprintf(os.Stderr, "warning: add UI lockdown rule: %v\n", err)
	} else {
		fmt.Fprintln(os.Stderr, "fw3 wan→18080 DROP rule installed")
	}
	return nil
}

// Uninstall removes cron, watchdog, kills daemon, drops uci rules.
// If keepConfig is false, the config dir + binary are removed too.
func Uninstall(keepConfig bool) error {
	// 1. Remove cron line
	if err := removeCronLine(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: remove cron line: %v\n", err)
	}
	if out, err := exec.Command("/etc/init.d/cron", "restart").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cron restart: %s: %v\n", strings.TrimSpace(string(out)), err)
	}

	// 2. Remove watchdog
	if err := os.Remove(config.WatchdogPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: remove watchdog: %v\n", err)
	}

	// 3. Kill daemon
	if data, err := os.ReadFile(config.PIDPath()); err == nil {
		var pid int
		fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
		if pid > 0 {
			_ = exec.Command("kill", "-15", fmt.Sprintf("%d", pid)).Run()
		}
		os.Remove(config.PIDPath())
	}
	_ = exec.Command("pkill", "-f", "wan6gate daemon").Run()

	// 4. Drop all uci firewall rules with our prefix (incl. UI lockdown)
	if err := firewall.DeleteAll(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: delete firewall rules: %v\n", err)
	}
	if err := firewall.DeleteUILockdownRule(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: delete UI lockdown rule: %v\n", err)
	}

	// 5. Optionally clean files
	if !keepConfig {
		_ = os.RemoveAll(config.BaseDir())
		_ = os.Remove(config.BinaryPath)
	}
	return nil
}

func ensureCronLine() error {
	body, err := os.ReadFile(config.CronFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(body)
	if strings.Contains(text, "wan6gate_watchdog.sh") {
		return nil
	}
	if len(text) > 0 && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += cronLine + "\n"
	return atomicWrite(config.CronFile, []byte(text), 0644)
}

func removeCronLine() error {
	body, err := os.ReadFile(config.CronFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var lines []string
	for _, l := range strings.Split(string(body), "\n") {
		if strings.Contains(l, "wan6gate_watchdog.sh") {
			continue
		}
		lines = append(lines, l)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") && out != "" {
		out += "\n"
	}
	return atomicWrite(config.CronFile, []byte(out), 0644)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
