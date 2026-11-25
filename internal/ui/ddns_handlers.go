package ui

import (
	"encoding/json"
	"net/http"

	"wan6gate/internal/config"
	"wan6gate/internal/ddns"
)

// handleDDNS GET returns the current DDNS config (including AccessKeySecret in
// plain text — the UI is LAN-only and authenticated, so masking just makes the
// config harder to inspect).
// PUT replaces user-controllable fields. An empty AccessKeySecret keeps the
// existing one so the form can be re-saved without re-typing the secret.
func (s *Server) handleDDNS(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, s.cfg.GetDDNS())
	case http.MethodPut:
		var body config.DDNSConfig
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		existing := s.cfg.GetDDNS()
		// Preserve existing secret if the form left it empty.
		if body.AccessKeySecret == "" {
			body.AccessKeySecret = existing.AccessKeySecret
		}
		if body.Provider == "" {
			body.Provider = "aliyun"
		}
		if body.TTL <= 0 {
			body.TTL = 600
		}
		if body.IntervalSeconds < 60 {
			body.IntervalSeconds = 300
		}
		if body.Enabled {
			if body.Domain == "" {
				writeErr(w, http.StatusBadRequest, "domain is required when enabled")
				return
			}
			if body.AccessKeyID == "" || body.AccessKeySecret == "" {
				writeErr(w, http.StatusBadRequest, "access_key_id and access_key_secret are required when enabled")
				return
			}
			if _, _, err := ddns.SplitDomain(body.Domain); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		// If domain changed, invalidate the cached RecordID so we re-bootstrap.
		if body.Domain != existing.Domain {
			body.RecordID = ""
		}
		s.cfg.SetDDNS(body)
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.log.Info("ddns config updated", "enabled", body.Enabled, "domain", body.Domain)
		// Trigger immediate sync if enabled
		if body.Enabled && s.ddnsMgr != nil {
			s.ddnsMgr.Trigger()
		}
		writeOK(w, s.cfg.GetDDNS())
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleDDNSSync forces an immediate sync attempt (uses currently-saved config).
func (s *Server) handleDDNSSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if s.ddnsMgr == nil {
		writeErr(w, http.StatusInternalServerError, "ddns manager not available")
		return
	}
	if err := s.ddnsMgr.SyncOnce(s.RunCtx()); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, s.cfg.GetDDNS())
}

// handleDDNSTest does a non-persistent credential test using DescribeDomainRecords.
// Useful from the UI before saving new credentials.
func (s *Server) handleDDNSTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body config.DDNSConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if body.AccessKeySecret == "" {
		body.AccessKeySecret = s.cfg.GetDDNS().AccessKeySecret
	}
	if body.AccessKeyID == "" || body.AccessKeySecret == "" {
		writeErr(w, http.StatusBadRequest, "credentials required")
		return
	}
	if body.Domain == "" {
		writeErr(w, http.StatusBadRequest, "domain required")
		return
	}
	zone, rr, err := ddns.SplitDomain(body.Domain)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	client := ddns.NewAliyunClient(body.AccessKeyID, body.AccessKeySecret)
	records, err := client.DescribeDomainRecords(zone, rr, "AAAA")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"zone":             zone,
		"rr":               rr,
		"existing_records": records,
	})
}
