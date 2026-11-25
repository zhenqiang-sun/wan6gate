package ui

import (
	"net/http"
	"strings"
)

// handleBanList returns the current ban + recent-fail snapshot.
func (s *Server) handleBanList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if s.bans == nil {
		writeOK(w, []interface{}{})
		return
	}
	writeOK(w, s.bans.Snapshot())
}

// handleBanListItem supports DELETE /api/banlist/<ip> to manually unban.
// IPv6 addresses contain ':' so we accept the rest-of-path verbatim.
func (s *Server) handleBanListItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "DELETE only")
		return
	}
	if s.bans == nil {
		writeErr(w, http.StatusServiceUnavailable, "ban list disabled")
		return
	}
	ip := strings.TrimPrefix(r.URL.Path, "/api/banlist/")
	if ip == "" {
		writeErr(w, http.StatusBadRequest, "ip required")
		return
	}
	s.bans.Unban(ip)
	s.log.Info("banlist manual unban", "ip", ip)
	writeOK(w, "unbanned")
}
