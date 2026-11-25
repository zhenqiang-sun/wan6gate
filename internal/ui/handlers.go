package ui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"wan6gate/internal/config"
	"wan6gate/internal/firewall"
	"wan6gate/internal/metrics"
	"wan6gate/internal/system"
	"wan6gate/internal/version"
)

type apiResp struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}

func writeOK(w http.ResponseWriter, data interface{}) {
	writeJSON(w, http.StatusOK, apiResp{OK: true, Data: data})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, apiResp{OK: false, Error: msg})
}

// --- setup ---

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]bool{"initialized": s.cfg.HasPasswordSet()})
}

func (s *Server) handleSetupInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if s.cfg.HasPasswordSet() {
		writeErr(w, http.StatusConflict, "already initialized")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if body.Username == "" {
		body.Username = "admin"
	}
	hash, err := config.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfg.SetUsername(body.Username)
	s.cfg.SetPassword(hash)
	if err := s.cfg.Save(s.cfgPath); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("admin password initialized", "username", body.Username)
	writeOK(w, nil)
}

// --- auth ---

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !s.cfg.HasPasswordSet() {
		writeErr(w, http.StatusPreconditionFailed, "not initialized")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if body.Username != s.cfg.GetUsername() ||
		!config.CheckPassword(s.cfg.Auth.PasswordBcrypt, body.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	tok, err := s.sessions.Create()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	setSessionCookie(w, tok)
	s.log.Info("login successful", "username", body.Username, "remote", r.RemoteAddr)
	writeOK(w, nil)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := getSessionToken(r); tok != "" {
		s.sessions.Revoke(tok)
	}
	clearSessionCookie(w)
	writeOK(w, nil)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if !config.CheckPassword(s.cfg.Auth.PasswordBcrypt, body.OldPassword) {
		writeErr(w, http.StatusUnauthorized, "wrong current password")
		return
	}
	hash, err := config.HashPassword(body.NewPassword)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cfg.SetPassword(hash)
	s.sessions.RevokeAll()
	if err := s.cfg.Save(s.cfgPath); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	clearSessionCookie(w)
	s.log.Info("password changed; all sessions revoked")
	writeOK(w, nil)
}

// --- status ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ip6, _ := system.RouterIPv6()
	mappings := s.cfg.ListMappings()
	active := 0
	for _, m := range mappings {
		if m.Enabled {
			active++
		}
	}
	writeOK(w, map[string]interface{}{
		"router_ipv6":     ip6,
		"username":        s.cfg.GetUsername(),
		"mappings_total":  len(mappings),
		"mappings_active": active,
		"started_at":      s.startedAt.Format(time.RFC3339),
		"version":         version.Full(),
	})
}

// --- mappings ---

// mappingWire wraps a Mapping with live metrics for UI display.
type mappingWire struct {
	*config.Mapping
	Stats metrics.Snapshot `json:"stats"`
}

func withMappingStats(list []*config.Mapping) []mappingWire {
	out := make([]mappingWire, 0, len(list))
	for _, m := range list {
		out = append(out, mappingWire{Mapping: m, Stats: metrics.Default.Get(m.ID).Snapshot()})
	}
	return out
}

func (s *Server) handleMappings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, withMappingStats(s.cfg.ListMappings()))
	case http.MethodPost:
		// Read body once so we can both probe for legacy "type" field AND parse it.
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Type == "socks5" {
			writeErr(w, http.StatusBadRequest, "SOCKS5 has been moved — POST to /api/socks5 instead")
			return
		}
		var m config.Mapping
		if err := json.Unmarshal(raw, &m); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		if m.ID == "" {
			m.ID = generateID()
		}
		now := time.Now().UTC()
		m.CreatedAt = now
		m.UpdatedAt = now
		if err := s.cfg.AddMapping(&m); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.applyMapping(&m)
		s.log.Info("mapping created", "id", m.ID, "proto", m.Protocol, "listen", m.ListenPort, "target", m.TargetIP, "enabled", m.Enabled)
		writeOK(w, &m)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMappingItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/mappings/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		m := s.cfg.FindMapping(id)
		if m == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeOK(w, m)
	case http.MethodPut, http.MethodPatch:
		existing := s.cfg.FindMapping(id)
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
		if v, ok := partial["protocol"].(string); ok {
			merged.Protocol = v
		}
		if v, ok := partial["listen_port"].(float64); ok {
			merged.ListenPort = int(v)
		}
		if v, ok := partial["target_ip"].(string); ok {
			merged.TargetIP = v
		}
		if v, ok := partial["target_port"].(float64); ok {
			merged.TargetPort = int(v)
		}
		if v, ok := partial["enabled"].(bool); ok {
			merged.Enabled = v
		}
		merged.UpdatedAt = time.Now().UTC()
		oldProto, oldPort, oldEnabled := existing.Protocol, existing.ListenPort, existing.Enabled
		if err := s.cfg.UpdateMapping(id, &merged); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Drop old firewall rule if (proto, port) changed or it was enabled and is now changed
		if oldProto != merged.Protocol || oldPort != merged.ListenPort {
			if err := firewall.DeleteRule(oldPort, oldProto); err != nil {
				s.log.Warn("delete old firewall rule failed", "err", err)
			}
		} else if oldEnabled && !merged.Enabled {
			// Was enabled, now disabled: drop firewall rule
			if err := firewall.DeleteRule(merged.ListenPort, merged.Protocol); err != nil {
				s.log.Warn("delete firewall rule failed", "err", err)
			}
		}
		s.applyMapping(&merged)
		s.log.Info("mapping updated", "id", merged.ID)
		writeOK(w, &merged)
	case http.MethodDelete:
		existing := s.cfg.FindMapping(id)
		if existing == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if err := s.cfg.DeleteMapping(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := firewall.DeleteRule(existing.ListenPort, existing.Protocol); err != nil {
			s.log.Warn("delete firewall rule failed", "err", err)
		}
		s.fwdMgr.Reconcile(s.RunCtx(), s.cfg.ListMappings())
		metrics.Default.Drop(id)
		s.log.Info("mapping deleted", "id", id)
		writeOK(w, nil)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// applyMapping reconciles forwarder + firewall for one mapping (and all others, since
// the manager reconciles the full set each time).
func (s *Server) applyMapping(m *config.Mapping) {
	if m.Enabled {
		if err := firewall.AddRule(m.ListenPort, m.Protocol); err != nil {
			s.log.Error("add firewall rule failed", "id", m.ID, "err", err)
		}
	}
	s.fwdMgr.Reconcile(s.RunCtx(), s.cfg.ListMappings())
}

func generateID() string {
	buf := make([]byte, 6)
	rand.Read(buf)
	return "m-" + hex.EncodeToString(buf)
}
