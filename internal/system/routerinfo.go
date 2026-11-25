package system

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// RouterInfo bundles read-once-per-request router state used by the UI overview.
// All fields degrade gracefully (empty string / zero) when unavailable, so the
// same struct shape works on the dev host (macOS) as on the OpenWrt target.
type RouterInfo struct {
	Hostname       string  `json:"hostname"`
	KernelRelease  string  `json:"kernel_release"`
	OSDescription  string  `json:"os_description"` // /etc/openwrt_release DISTRIB_DESCRIPTION
	OSTarget       string  `json:"os_target"`      // /etc/openwrt_release DISTRIB_TARGET
	UptimeSeconds  int64   `json:"uptime_seconds"` // /proc/uptime first column
	Load1          float64 `json:"load_1"`
	Load5          float64 `json:"load_5"`
	Load15         float64 `json:"load_15"`
	MemTotalKB     int64   `json:"mem_total_kb"`     // /proc/meminfo MemTotal
	MemAvailableKB int64   `json:"mem_available_kb"` // /proc/meminfo MemAvailable
	GoOS           string  `json:"go_os"`            // runtime.GOOS, useful when running on macOS for dev
	GoArch         string  `json:"go_arch"`
}

// CollectRouterInfo gathers what's available; never returns an error.
// On non-Linux hosts, only Hostname / GoOS / GoArch are populated.
func CollectRouterInfo() RouterInfo {
	info := RouterInfo{
		GoOS:   runtime.GOOS,
		GoArch: runtime.GOARCH,
	}
	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	if runtime.GOOS != "linux" {
		return info
	}
	info.KernelRelease = readTrim("/proc/sys/kernel/osrelease")
	if info.Hostname == "" {
		info.Hostname = readTrim("/proc/sys/kernel/hostname")
	}
	parseOpenWrtRelease(&info)
	parseProcUptime(&info)
	parseProcLoadavg(&info)
	parseProcMeminfo(&info)
	return info
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func parseOpenWrtRelease(info *RouterInfo) {
	f, err := os.Open("/etc/openwrt_release")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, "'\"")
		switch k {
		case "DISTRIB_DESCRIPTION":
			// MiWiFi-flavoured firmwares often leave DISTRIB_DESCRIPTION as
			// "OpenWrt 18.06-SNAPSHOT unknown" — the trailing "unknown" is
			// the codename placeholder. Strip it so the UI looks clean.
			info.OSDescription = strings.TrimSpace(strings.TrimSuffix(v, " unknown"))
		case "DISTRIB_TARGET":
			info.OSTarget = v
		}
	}
}

func parseProcUptime(info *RouterInfo) {
	s := readTrim("/proc/uptime")
	if s == "" {
		return
	}
	first, _, _ := strings.Cut(s, " ")
	if f, err := strconv.ParseFloat(first, 64); err == nil {
		info.UptimeSeconds = int64(f)
	}
}

func parseProcLoadavg(info *RouterInfo) {
	s := readTrim("/proc/loadavg")
	if s == "" {
		return
	}
	fields := strings.Fields(s)
	if len(fields) >= 3 {
		info.Load1, _ = strconv.ParseFloat(fields[0], 64)
		info.Load5, _ = strconv.ParseFloat(fields[1], 64)
		info.Load15, _ = strconv.ParseFloat(fields[2], 64)
	}
}

func parseProcMeminfo(info *RouterInfo) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest) // e.g. ["123456", "kB"]
		if len(fields) == 0 {
			continue
		}
		val, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "MemTotal":
			info.MemTotalKB = val
		case "MemAvailable":
			info.MemAvailableKB = val
		}
	}
}
