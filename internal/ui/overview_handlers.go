package ui

import (
	"net/http"
	"os"
	"time"

	"wan6gate/internal/metrics"
	"wan6gate/internal/system"
	"wan6gate/internal/version"
)

// overviewResponse is the shape consumed by the front-end "概览" section.
type overviewResponse struct {
	Daemon   daemonOverview   `json:"daemon"`
	Router   routerOverview   `json:"router"`
	Network  networkOverview  `json:"network"`
	Services servicesOverview `json:"services"`
	Traffic  trafficOverview  `json:"traffic"`
	DDNS     ddnsOverview     `json:"ddns"`
	Notify   notifyOverview   `json:"notify"`
}

type daemonOverview struct {
	Version       string `json:"version"`
	Number        string `json:"number"`
	Commit        string `json:"commit"`
	BuildTime     string `json:"build_time"`
	StartedAt     string `json:"started_at"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	PID           int    `json:"pid"`
	ConfigVersion int    `json:"config_version"`
	Username      string `json:"username"`
}

type routerOverview struct {
	Hostname       string  `json:"hostname"`
	KernelRelease  string  `json:"kernel_release"`
	OSDescription  string  `json:"os_description"`
	OSTarget       string  `json:"os_target"`
	UptimeSeconds  int64   `json:"uptime_seconds"`
	Load1          float64 `json:"load_1"`
	Load5          float64 `json:"load_5"`
	Load15         float64 `json:"load_15"`
	MemTotalKB     int64   `json:"mem_total_kb"`
	MemAvailableKB int64   `json:"mem_available_kb"`
	GoOS           string  `json:"go_os"`
	GoArch         string  `json:"go_arch"`
}

type networkOverview struct {
	WANIPv6    string `json:"wan_ipv6"`
	UIBindAddr string `json:"ui_bind_addr"`
}

type servicesOverview struct {
	MappingsTotal  int `json:"mappings_total"`
	MappingsActive int `json:"mappings_active"`
	Socks5Total    int `json:"socks5_total"`
	Socks5Active   int `json:"socks5_active"`
}

type trafficOverview struct {
	ActiveConnections int64 `json:"active_connections"`
	TotalAccepted     int64 `json:"total_accepted"`
	TotalErrors       int64 `json:"total_errors"`
	BytesIn           int64 `json:"bytes_in"`
	BytesOut          int64 `json:"bytes_out"`
}

type ddnsOverview struct {
	Enabled         bool   `json:"enabled"`
	Domain          string `json:"domain"`
	IntervalSeconds int    `json:"interval_seconds"`
	LastSyncedIP    string `json:"last_synced_ip"`
	LastSyncedAt    string `json:"last_synced_at"`
	LastError       string `json:"last_error"`
}

type notifyOverview struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	mappings := s.cfg.ListMappings()
	mappingsActive := 0
	for _, m := range mappings {
		if m.Enabled {
			mappingsActive++
		}
	}
	socksList := s.cfg.ListSocks5()
	socksActive := 0
	for _, sv := range socksList {
		if sv.Enabled {
			socksActive++
		}
	}

	traffic := trafficOverview{}
	for _, snap := range metrics.Default.SnapshotAll() {
		traffic.ActiveConnections += snap.Active
		traffic.TotalAccepted += snap.Accepted
		traffic.TotalErrors += snap.Errors
		traffic.BytesIn += snap.BytesIn
		traffic.BytesOut += snap.BytesOut
	}

	ip6, _ := system.RouterIPv6()
	ri := system.CollectRouterInfo()
	dd := s.cfg.GetDDNS()
	nt := s.cfg.GetNotify()

	resp := overviewResponse{
		Daemon: daemonOverview{
			Version:       version.Full(),
			Number:        version.Number,
			Commit:        version.Commit,
			BuildTime:     version.BuildTime,
			StartedAt:     s.startedAt.UTC().Format(time.RFC3339),
			UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
			PID:           os.Getpid(),
			ConfigVersion: s.cfg.Version,
			Username:      s.cfg.GetUsername(),
		},
		Router: routerOverview{
			Hostname:       ri.Hostname,
			KernelRelease:  ri.KernelRelease,
			OSDescription:  ri.OSDescription,
			OSTarget:       ri.OSTarget,
			UptimeSeconds:  ri.UptimeSeconds,
			Load1:          ri.Load1,
			Load5:          ri.Load5,
			Load15:         ri.Load15,
			MemTotalKB:     ri.MemTotalKB,
			MemAvailableKB: ri.MemAvailableKB,
			GoOS:           ri.GoOS,
			GoArch:         ri.GoArch,
		},
		Network: networkOverview{
			WANIPv6:    ip6,
			UIBindAddr: s.cfg.UI.BindAddr,
		},
		Services: servicesOverview{
			MappingsTotal:  len(mappings),
			MappingsActive: mappingsActive,
			Socks5Total:    len(socksList),
			Socks5Active:   socksActive,
		},
		Traffic: traffic,
		DDNS: ddnsOverview{
			Enabled:         dd.Enabled,
			Domain:          dd.Domain,
			IntervalSeconds: dd.IntervalSeconds,
			LastSyncedIP:    dd.LastSyncedIP,
			LastSyncedAt:    dd.LastSyncedAt,
			LastError:       dd.LastError,
		},
		Notify: notifyOverview{
			Enabled: nt.Enabled,
		},
	}
	writeOK(w, resp)
}
