package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Socks5Server defines a single authenticated SOCKS5 listener.
// Currently TCP CONNECT only (no UDP ASSOCIATE — see P6 plan for rationale).
type Socks5Server struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	ListenPort int       `json:"listen_port"`
	Username   string    `json:"username"`
	Password   string    `json:"password"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s *Socks5Server) Validate() error {
	if !validIDRegex.MatchString(s.ID) {
		return errors.New("id must match [a-zA-Z0-9_-]{1,32}")
	}
	if len(s.Label) > 64 {
		return errors.New("label too long (>64)")
	}
	if s.ListenPort < 1 || s.ListenPort > 65535 {
		return fmt.Errorf("listen_port out of range: %d", s.ListenPort)
	}
	if reason, forbid := ForbiddenListenPorts[s.ListenPort]; forbid {
		return fmt.Errorf("listen_port %d is reserved by router (%s)", s.ListenPort, reason)
	}
	if strings.TrimSpace(s.Username) == "" || s.Password == "" {
		return errors.New("socks5 requires non-empty username and password (no NoAuth allowed)")
	}
	if len(s.Username) > 255 || len(s.Password) > 255 {
		return errors.New("socks5 username/password length must each be ≤ 255 (RFC 1929)")
	}
	return nil
}
