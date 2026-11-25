package ui

import (
	"net/http"
	"strings"

	"wan6gate/internal/clientconfig"
)

// handleSocks5ClientConfig — GET /api/socks5-clientconfig/<id>?format=...
// Returns the raw config text. Path uses '/api/socks5-clientconfig/' prefix
// to avoid colliding with /api/socks5/<id>.
func (s *Server) handleSocks5ClientConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/socks5-clientconfig/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	srv := s.cfg.FindSocks5(id)
	if srv == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "uri"
	}

	host := s.proxyHost()
	tag := "wan6gate-" + sanitizeTag(srv.Username)
	p := clientconfig.Params{
		Tag:      tag,
		Host:     host,
		Port:     srv.ListenPort,
		Username: srv.Username,
		Password: srv.Password,
	}

	var out, mime, filename string
	switch format {
	case "uri":
		out, mime, filename = clientconfig.URI(p), "text/plain", tag+".txt"
	case "singbox":
		out, mime, filename = clientconfig.SingBox(p), "application/json", tag+".singbox.json"
	case "clash":
		out, mime, filename = clientconfig.Clash(p), "text/yaml", tag+".clash.yaml"
	case "surge":
		out, mime, filename = clientconfig.Surge(p), "text/plain", tag+".surge.conf"
	default:
		writeErr(w, http.StatusBadRequest, "format must be one of uri|singbox|clash|surge")
		return
	}

	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	}
	w.Header().Set("Content-Type", mime+"; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(out))
}

// proxyHost picks the best public host: DDNS domain if enabled, else raw GUA.
func (s *Server) proxyHost() string {
	d := s.cfg.GetDDNS()
	if d.Enabled && d.Domain != "" {
		return d.Domain
	}
	if ip, err := routerIPv6(); err == nil && ip != "" {
		return ip
	}
	return "<router-host>"
}

// sanitizeTag strips characters that would break YAML/JSON tags (very loose).
func sanitizeTag(s string) string {
	if s == "" {
		return "user"
	}
	out := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "user"
	}
	return string(out)
}
