package ui

import (
	"net/http"
	"strconv"
	"time"

	"wan6gate/internal/historystore"
)

// handleHistory returns time-series points for the given service.
//
//	GET /api/history?id=<svc>&hours=24
//
// Output: { "labels": ["10:00",...], "bytes_in_delta": [...], "bytes_out_delta": [...], "accepted_delta": [...] }
// Deltas are per-snapshot (hourly) increments. Restart resets counters → first
// point after a restart can show as 0 (counter went down, we clamp to 0).
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if s.history == nil {
		writeErr(w, http.StatusServiceUnavailable, "history store not initialized")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 24*90 {
			hours = n
		}
	}
	to := time.Now().UTC()
	from := to.Add(-time.Duration(hours) * time.Hour)
	snaps, err := s.history.QueryRange(from, to, map[string]bool{id: true})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	labels := make([]string, 0, len(snaps))
	bytesIn := make([]int64, 0, len(snaps))
	bytesOut := make([]int64, 0, len(snaps))
	accepted := make([]int64, 0, len(snaps))
	var prev *historystore.CounterRecord
	for _, sn := range snaps {
		labels = append(labels, time.Unix(sn.TS, 0).Local().Format("01-02 15:04"))
		cur := sn.Services[id]
		bytesIn = append(bytesIn, deltaCounter(prev, cur, "bytes_in"))
		bytesOut = append(bytesOut, deltaCounter(prev, cur, "bytes_out"))
		accepted = append(accepted, deltaCounter(prev, cur, "accepted"))
		c := cur
		prev = &c
	}
	writeOK(w, map[string]interface{}{
		"labels":          labels,
		"bytes_in_delta":  bytesIn,
		"bytes_out_delta": bytesOut,
		"accepted_delta":  accepted,
	})
}

// deltaCounter returns max(cur - prev, 0); the clamp handles restart resets.
func deltaCounter(prev *historystore.CounterRecord, cur historystore.CounterRecord, field string) int64 {
	getter := func(c historystore.CounterRecord) int64 {
		switch field {
		case "bytes_in":
			return c.BytesIn
		case "bytes_out":
			return c.BytesOut
		case "accepted":
			return c.Accepted
		}
		return 0
	}
	if prev == nil {
		return 0
	}
	d := getter(cur) - getter(*prev)
	if d < 0 {
		return 0
	}
	return d
}
