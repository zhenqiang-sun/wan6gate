package config

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// Mapping is a port-forward rule: external IPv6 listen → fixed LAN target.
// SOCKS5 is NOT a Mapping any more (since v4) — see Socks5Server.
type Mapping struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Protocol   string    `json:"protocol"` // "tcp" or "udp"
	ListenPort int       `json:"listen_port"`
	TargetIP   string    `json:"target_ip"`
	TargetPort int       `json:"target_port"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

var validIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

// ForbiddenListenPorts are router-essential ports the user must not bind to.
var ForbiddenListenPorts = map[int]string{
	22:    "SSH (dropbear)",
	23:    "telnet",
	53:    "DNS (dnsmasq)",
	67:    "DHCP server",
	80:    "HTTP (mi web UI)",
	443:   "HTTPS (mi web UI)",
	8080:  "HTTP alt (mi web UI)",
	8098:  "HTTP alt (mi web UI)",
	8099:  "HTTPS alt (mi nginx)",
	8883:  "mosquitto MQTT",
	18080: "wan6gate UI",
	139:   "SMB",
	445:   "SMB",
}

func (m *Mapping) Validate() error {
	if !validIDRegex.MatchString(m.ID) {
		return errors.New("id must match [a-zA-Z0-9_-]{1,32}")
	}
	if len(m.Label) > 64 {
		return errors.New("label too long (>64)")
	}
	if m.ListenPort < 1 || m.ListenPort > 65535 {
		return fmt.Errorf("listen_port out of range: %d", m.ListenPort)
	}
	if reason, forbid := ForbiddenListenPorts[m.ListenPort]; forbid {
		return fmt.Errorf("listen_port %d is reserved by router (%s)", m.ListenPort, reason)
	}
	p := strings.ToLower(strings.TrimSpace(m.Protocol))
	if p != "tcp" && p != "udp" {
		return errors.New("protocol must be tcp or udp")
	}
	m.Protocol = p
	if m.TargetPort < 1 || m.TargetPort > 65535 {
		return fmt.Errorf("target_port out of range: %d", m.TargetPort)
	}
	if net.ParseIP(strings.TrimSpace(m.TargetIP)) == nil {
		return fmt.Errorf("target_ip invalid: %q", m.TargetIP)
	}
	m.TargetIP = strings.TrimSpace(m.TargetIP)
	return nil
}
