package ui

import (
	"net/http"
	"runtime"
	"time"

	"wan6gate/internal/metrics"
)

// handleMetrics returns a snapshot of all in-memory counters + global state.
// In-memory only; resets on daemon restart.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	// Build per-service map keyed by ID. The UI matches IDs back to mappings/socks5.
	per := metrics.Default.SnapshotAll()

	// Tag each entry with what service it is (forward / socks5) for UI display.
	type tagged struct {
		Kind  string           `json:"kind"`
		Stats metrics.Snapshot `json:"stats"`
	}
	tagMap := make(map[string]tagged, len(per))
	for _, m := range s.cfg.ListMappings() {
		if snap, ok := per[m.ID]; ok {
			tagMap[m.ID] = tagged{Kind: "forward", Stats: snap}
		}
	}
	for _, sv := range s.cfg.ListSocks5() {
		if snap, ok := per[sv.ID]; ok {
			tagMap[sv.ID] = tagged{Kind: "socks5", Stats: snap}
		}
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	writeOK(w, map[string]interface{}{
		"per_service":    tagMap,
		"uptime_seconds": int64(time.Since(s.startedAt).Seconds()),
		"started_at":     s.startedAt.UTC().Format(time.RFC3339),
		"goroutines":     runtime.NumGoroutine(),
		"memory_alloc":   mem.Alloc,
		"memory_sys":     mem.Sys,
		"router_ipv6":    getRouterIPv6Cached(s),
	})
}

// getRouterIPv6Cached avoids a syscall per /api/metrics request — though the
// underlying read is cheap (it parses /proc), we still amortize. For now,
// just call through; if metrics gets polled fast we can add a cache later.
func getRouterIPv6Cached(s *Server) string {
	// Reuse the function we already export via /api/status.
	// The cost is one /proc read; trivial.
	ip, _ := routerIPv6()
	return ip
}
