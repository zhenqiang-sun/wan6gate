// Package clientconfig renders SOCKS5 client config snippets in several
// popular formats: sing-box, Clash (and Mihomo), Surge, and a generic URI.
package clientconfig

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// Params is the input bundle. Host should be a public-resolvable address
// (DDNS domain preferred over raw IPv6 GUA).
type Params struct {
	Tag      string // human-friendly proxy name, e.g. "wan6gate-home"
	Host     string // hostname OR raw IPv6 (no brackets)
	Port     int
	Username string
	Password string
}

// hostForBracketed returns the host either bare (FQDN/IPv4) or bracketed (IPv6).
func (p Params) hostForBracketed() string {
	if ip := net.ParseIP(p.Host); ip != nil && ip.To4() == nil {
		return "[" + p.Host + "]"
	}
	return p.Host
}

// URI returns the generic socks5h:// URI (works with curl, browsers, etc.).
func URI(p Params) string {
	u := &url.URL{
		Scheme: "socks5h",
		User:   url.UserPassword(p.Username, p.Password),
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
	}
	return u.String()
}

// SingBox returns a sing-box outbound JSON object.
func SingBox(p Params) string {
	cfg := map[string]interface{}{
		"type":        "socks",
		"tag":         p.Tag,
		"server":      p.Host,
		"server_port": p.Port,
		"version":     "5",
		"username":    p.Username,
		"password":    p.Password,
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return string(b)
}

// Clash returns a Clash / Mihomo proxy YAML stanza.
func Clash(p Params) string {
	return fmt.Sprintf(`- name: %s
  type: socks5
  server: %s
  port: %d
  username: %s
  password: %s
  udp: false
`, p.Tag, p.Host, p.Port, p.Username, escapeYAML(p.Password))
}

// Surge returns a Surge [Proxy] entry line.
func Surge(p Params) string {
	return fmt.Sprintf("%s = socks5, %s, %d, %s, %s\n",
		p.Tag, p.Host, p.Port, p.Username, p.Password)
}

// escapeYAML wraps strings that contain YAML-significant characters in quotes.
func escapeYAML(s string) string {
	for _, c := range s {
		if c == '"' || c == '\'' || c == ':' || c == '#' || c == ' ' || c == '\n' {
			b, _ := json.Marshal(s) // produces "..." JSON string, valid YAML
			return string(b)
		}
	}
	return s
}
