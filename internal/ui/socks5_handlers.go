package ui

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"wan6gate/internal/config"
	"wan6gate/internal/firewall"
	"wan6gate/internal/metrics"
)

// handleSocks5List handles /api/socks5 (collection).
func (s *Server) handleSocks5List(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, withSocks5Stats(s.cfg.ListSocks5()))
	case http.MethodPost:
		var srv config.Socks5Server
		if err := json.NewDecoder(r.Body).Decode(&srv); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		if srv.ID == "" {
			srv.ID = generateSocks5ID()
		}
		now := time.Now().UTC()
		srv.CreatedAt = now
		srv.UpdatedAt = now
		if err := s.cfg.AddSocks5(&srv); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.applySocks5(&srv)
		s.log.Info("socks5 created", "id", srv.ID, "listen", srv.ListenPort, "user", srv.Username, "enabled", srv.Enabled)
		writeOK(w, &srv)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSocks5Item handles /api/socks5/<id>.
func (s *Server) handleSocks5Item(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/socks5/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		srv := s.cfg.FindSocks5(id)
		if srv == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeOK(w, srv)
	case http.MethodPut, http.MethodPatch:
		existing := s.cfg.FindSocks5(id)
		if existing == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		var partial map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&partial); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		merged := *existing
		if v, ok := partial["label"].(string); ok {
			merged.Label = v
		}
		if v, ok := partial["listen_port"].(float64); ok {
			merged.ListenPort = int(v)
		}
		if v, ok := partial["username"].(string); ok && v != "" {
			merged.Username = v
		}
		if v, ok := partial["password"].(string); ok && v != "" {
			merged.Password = v
		}
		if v, ok := partial["enabled"].(bool); ok {
			merged.Enabled = v
		}
		merged.UpdatedAt = time.Now().UTC()
		oldPort, oldEnabled := existing.ListenPort, existing.Enabled
		if err := s.cfg.UpdateSocks5(id, &merged); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Drop old firewall rule if port changed or service was just disabled.
		if oldPort != merged.ListenPort {
			if err := firewall.DeleteRule(oldPort, "tcp"); err != nil {
				s.log.Warn("delete old firewall rule failed", "err", err)
			}
		} else if oldEnabled && !merged.Enabled {
			if err := firewall.DeleteRule(merged.ListenPort, "tcp"); err != nil {
				s.log.Warn("delete firewall rule failed", "err", err)
			}
		}
		s.applySocks5(&merged)
		s.log.Info("socks5 updated", "id", merged.ID)
		writeOK(w, &merged)
	case http.MethodDelete:
		existing := s.cfg.FindSocks5(id)
		if existing == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if err := s.cfg.DeleteSocks5(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := firewall.DeleteRule(existing.ListenPort, "tcp"); err != nil {
			s.log.Warn("delete firewall rule failed", "err", err)
		}
		s.socksMgr.Reconcile(s.RunCtx(), s.cfg.ListSocks5())
		metrics.Default.Drop(id)
		s.log.Info("socks5 deleted", "id", id)
		writeOK(w, nil)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// applySocks5 mirrors applyMapping but for the SOCKS5 manager.
func (s *Server) applySocks5(srv *config.Socks5Server) {
	if srv.Enabled {
		if err := firewall.AddRule(srv.ListenPort, "tcp"); err != nil {
			s.log.Error("add firewall rule failed", "id", srv.ID, "err", err)
		}
	}
	s.socksMgr.Reconcile(s.RunCtx(), s.cfg.ListSocks5())
}

func generateSocks5ID() string {
	return generateID() // reuse same generator; "m-" prefix is fine for both
}

// socks5Wire is the wire shape returned to UI: includes live stats.
type socks5Wire struct {
	*config.Socks5Server
	Stats metrics.Snapshot `json:"stats"`
}

func withSocks5Stats(list []*config.Socks5Server) []socks5Wire {
	out := make([]socks5Wire, 0, len(list))
	for _, s := range list {
		out = append(out, socks5Wire{Socks5Server: s, Stats: metrics.Default.Get(s.ID).Snapshot()})
	}
	return out
}
