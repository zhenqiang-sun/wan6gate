package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type UIConfig struct {
	BindAddr      string `json:"bind_addr"`
	SessionSecret string `json:"session_secret"`
}

type AuthCfg struct {
	Username       string `json:"username"`
	PasswordBcrypt string `json:"password_bcrypt"`
}

// DDNSConfig holds dynamic DNS settings for one record.
// AccessKeySecret is plain-text in API responses — the UI is LAN-only and
// authenticated, so masking just makes config harder to inspect.
type DDNSConfig struct {
	Enabled         bool   `json:"enabled"`
	Provider        string `json:"provider"` // "aliyun" only for now
	Domain          string `json:"domain"`   // FQDN, e.g. "home.example.com"
	TTL             int    `json:"ttl"`
	IntervalSeconds int    `json:"interval_seconds"`
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`

	// runtime state, persisted for resume across restart
	RecordID     string `json:"record_id"`
	LastSyncedAt string `json:"last_synced_at"`
	LastSyncedIP string `json:"last_synced_ip"`
	LastError    string `json:"last_error"`
}

// NotifyConfig configures outbound webhook notifications.
// WebhookURL is returned in plain text — the UI is LAN-only and authenticated.
type NotifyConfig struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider"` // "feishu" only for now
	WebhookURL string `json:"webhook_url"`

	// Per-event toggles. Empty defaults to all-on.
	NotifyOnDaemonStart bool `json:"notify_on_daemon_start"`
	NotifyOnDDNSChange  bool `json:"notify_on_ddns_change"`
	NotifyOnDDNSError   bool `json:"notify_on_ddns_error"`
	NotifyOnSocks5Ban   bool `json:"notify_on_socks5_ban"`
}

type Config struct {
	Version       int             `json:"version"`
	UI            UIConfig        `json:"ui"`
	Auth          AuthCfg         `json:"auth"`
	Mappings      []*Mapping      `json:"mappings"`       // forward only
	Socks5Servers []*Socks5Server `json:"socks5_servers"` // independent of mappings
	DDNS          DDNSConfig      `json:"ddns"`
	Notify        NotifyConfig    `json:"notify"`
	Tasks         []*Task         `json:"tasks"`

	mu sync.RWMutex `json:"-"`
}

// legacyMapping is the v3 union shape for migration. It is read-only — never
// written by the current code. Kept here to deserialize old configs.
type legacyMapping struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Type       string    `json:"type"`
	Protocol   string    `json:"protocol"`
	ListenPort int       `json:"listen_port"`
	TargetIP   string    `json:"target_ip,omitempty"`
	TargetPort int       `json:"target_port,omitempty"`
	Username   string    `json:"username,omitempty"`
	Password   string    `json:"password,omitempty"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// legacyConfig is the union shape we read on Load to detect v3 → v4.
type legacyConfig struct {
	Version  int              `json:"version"`
	Mappings []*legacyMapping `json:"mappings"`
}

func New() *Config {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		// Fallback shouldn't happen in practice.
		copy(secret, []byte("DETERMINISTIC_FALLBACK_NOT_SECURE!"))
	}
	return &Config{
		Version: 5,
		UI: UIConfig{
			// Wildcard bind so LAN renumbering doesn't break the UI.
			// fw3 zone_wan_input default REJECT plus our explicit
			// wan6gate_ui_lockdown rule keep this off the WAN.
			BindAddr:      "0.0.0.0:18080",
			SessionSecret: hex.EncodeToString(secret),
		},
		Auth:          AuthCfg{Username: "admin"},
		Mappings:      []*Mapping{},
		Socks5Servers: []*Socks5Server{},
		Tasks:         []*Task{},
		DDNS: DDNSConfig{
			Provider:        "aliyun",
			TTL:             600,
			IntervalSeconds: 300,
		},
		Notify: NotifyConfig{
			Provider:            "feishu",
			NotifyOnDaemonStart: true,
			NotifyOnDDNSChange:  true,
			NotifyOnDDNSError:   true,
			NotifyOnSocks5Ban:   true,
		},
	}
}

// Load reads the config file. Returns a fresh config if file doesn't exist.
// On v3→v4 detection, migrates legacy mappings (type=socks5) into Socks5Servers
// and writes a backup of the original config alongside.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}

	// First, parse with legacy schema to detect & extract v3 socks5-typed mappings.
	var legacy legacyConfig
	_ = json.Unmarshal(data, &legacy)
	migrated := false

	c := &Config{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	if c.Version == 0 {
		c.Version = 1
	}

	// v3 → v4: split mappings by type
	if c.Version < 4 && hasLegacySocks5(legacy.Mappings) {
		_ = backupConfig(path, data)
		var keep []*Mapping
		for i, lm := range legacy.Mappings {
			if lm.Type == "socks5" {
				c.Socks5Servers = append(c.Socks5Servers, &Socks5Server{
					ID:         lm.ID,
					Label:      lm.Label,
					ListenPort: lm.ListenPort,
					Username:   lm.Username,
					Password:   lm.Password,
					Enabled:    lm.Enabled,
					CreatedAt:  lm.CreatedAt,
					UpdatedAt:  lm.UpdatedAt,
				})
			} else {
				// reuse the slot from c.Mappings to preserve identity (defensive)
				if i < len(c.Mappings) && c.Mappings[i] != nil {
					keep = append(keep, c.Mappings[i])
				}
			}
		}
		c.Mappings = keep
		migrated = true
	}
	if c.UI.SessionSecret == "" {
		secret := make([]byte, 32)
		rand.Read(secret)
		c.UI.SessionSecret = hex.EncodeToString(secret)
	}
	if c.UI.BindAddr == "" {
		c.UI.BindAddr = "0.0.0.0:18080"
	}
	if c.Auth.Username == "" {
		c.Auth.Username = "admin"
	}
	if c.Mappings == nil {
		c.Mappings = []*Mapping{}
	}
	if c.Socks5Servers == nil {
		c.Socks5Servers = []*Socks5Server{}
	}
	if c.Tasks == nil {
		c.Tasks = []*Task{}
	}
	// DDNS section defaults (covers v1 → v2 upgrade)
	if c.DDNS.Provider == "" {
		c.DDNS.Provider = "aliyun"
	}
	if c.DDNS.TTL == 0 {
		c.DDNS.TTL = 600
	}
	if c.DDNS.IntervalSeconds == 0 {
		c.DDNS.IntervalSeconds = 300
	}
	// Notify section defaults (covers v2 → v3 upgrade)
	if c.Notify.Provider == "" {
		c.Notify.Provider = "feishu"
	}
	if c.Version < 3 {
		// First upgrade to v3: enable per-event flags by default if zero
		// (json zero-value bool can't be distinguished from explicit false,
		// so we use Version as the upgrade marker).
		c.Notify.NotifyOnDaemonStart = true
		c.Notify.NotifyOnDDNSChange = true
		c.Notify.NotifyOnDDNSError = true
		c.Version = 3
	}
	if c.Version < 4 {
		c.Version = 4
	}
	if c.Version < 5 {
		// v4 → v5: introduce socks5 ban notification toggle, default on so
		// existing users get the new alert without re-opening settings.
		c.Notify.NotifyOnSocks5Ban = true
		c.Version = 5
	}
	if migrated {
		// Caller will call Save shortly; nothing extra needed here.
		_ = migrated
	}
	return c, nil
}

func hasLegacySocks5(ms []*legacyMapping) bool {
	for _, m := range ms {
		if m != nil && m.Type == "socks5" {
			return true
		}
	}
	return false
}

func backupConfig(path string, data []byte) error {
	bak := path + ".v3.bak"
	if _, err := os.Stat(bak); err == nil {
		// already have one; don't overwrite (preserve original first migration moment)
		return nil
	}
	return os.WriteFile(bak, data, 0600)
}

// GetDDNS returns a copy of the DDNS section.
func (c *Config) GetDDNS() DDNSConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.DDNS
}

// SetDDNS replaces the user-controllable DDNS settings.
// It preserves runtime state (RecordID, LastSyncedAt/IP, LastError) from the
// existing config — caller updates those via SetDDNSStatus.
func (c *Config) SetDDNS(d DDNSConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d.RecordID = c.DDNS.RecordID
	d.LastSyncedAt = c.DDNS.LastSyncedAt
	d.LastSyncedIP = c.DDNS.LastSyncedIP
	d.LastError = c.DDNS.LastError
	c.DDNS = d
}

// SetDDNSStatus updates the runtime state fields. errMsg=="" clears error.
// recordID=="" leaves RecordID untouched. ip=="" leaves LastSyncedIP untouched.
func (c *Config) SetDDNSStatus(ip, recordID, errMsg, atRFC3339 string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if recordID != "" {
		c.DDNS.RecordID = recordID
	}
	if ip != "" {
		c.DDNS.LastSyncedIP = ip
	}
	c.DDNS.LastError = errMsg
	if atRFC3339 != "" {
		c.DDNS.LastSyncedAt = atRFC3339
	}
}

// Save writes atomically (tmp+rename), mode 0600.
func (c *Config) Save(path string) error {
	c.mu.RLock()
	data, err := json.MarshalIndent(c, "", "  ")
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) FindMapping(id string) *Mapping {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, m := range c.Mappings {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// AddMapping appends, validating uniqueness of (proto, listen_port).
func (c *Config) AddMapping(m *Mapping) error {
	if err := m.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, x := range c.Mappings {
		if x.ID == m.ID {
			return fmt.Errorf("id %q already exists", m.ID)
		}
		if x.Protocol == m.Protocol && x.ListenPort == m.ListenPort {
			return fmt.Errorf("listen_port %d/%s already used by mapping %q",
				m.ListenPort, m.Protocol, x.ID)
		}
	}
	c.Mappings = append(c.Mappings, m)
	return nil
}

// UpdateMapping replaces an existing mapping by ID.
func (c *Config) UpdateMapping(id string, updated *Mapping) error {
	updated.ID = id
	if err := updated.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, x := range c.Mappings {
		if x.ID != id {
			continue
		}
		for j, y := range c.Mappings {
			if i == j {
				continue
			}
			if y.Protocol == updated.Protocol && y.ListenPort == updated.ListenPort {
				return fmt.Errorf("listen_port %d/%s already used by mapping %q",
					updated.ListenPort, updated.Protocol, y.ID)
			}
		}
		updated.CreatedAt = x.CreatedAt
		c.Mappings[i] = updated
		return nil
	}
	return fmt.Errorf("mapping %q not found", id)
}

func (c *Config) DeleteMapping(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, x := range c.Mappings {
		if x.ID == id {
			c.Mappings = append(c.Mappings[:i], c.Mappings[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("mapping %q not found", id)
}

func (c *Config) ListMappings() []*Mapping {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Mapping, len(c.Mappings))
	copy(out, c.Mappings)
	return out
}

func (c *Config) HasPasswordSet() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Auth.PasswordBcrypt != ""
}

func (c *Config) SetPassword(hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Auth.PasswordBcrypt = hash
}

func (c *Config) GetUsername() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Auth.Username
}

func (c *Config) SetUsername(u string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Auth.Username = u
}

// GetNotify returns a copy of the notify section.
func (c *Config) GetNotify() NotifyConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Notify
}

// SetNotify replaces the notify section.
func (c *Config) SetNotify(n NotifyConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Notify = n
}

// SetUIBindAddr changes the UI bind address. Caller is responsible for restarting
// the HTTP server after persisting.
func (c *Config) SetUIBindAddr(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.UI.BindAddr = addr
}

// --- Socks5Server CRUD ---

func (c *Config) ListSocks5() []*Socks5Server {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Socks5Server, len(c.Socks5Servers))
	copy(out, c.Socks5Servers)
	return out
}

func (c *Config) FindSocks5(id string) *Socks5Server {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, s := range c.Socks5Servers {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// AddSocks5 appends a new SOCKS5 listener. Validates uniqueness of (tcp, listen_port)
// across both forwards and other socks5 servers.
func (c *Config) AddSocks5(s *Socks5Server) error {
	if err := s.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, x := range c.Mappings {
		if x.Protocol == "tcp" && x.ListenPort == s.ListenPort {
			return fmt.Errorf("listen_port %d/tcp already used by mapping %q", s.ListenPort, x.ID)
		}
	}
	for _, x := range c.Socks5Servers {
		if x.ID == s.ID {
			return fmt.Errorf("id %q already exists", s.ID)
		}
		if x.ListenPort == s.ListenPort {
			return fmt.Errorf("listen_port %d/tcp already used by socks5 %q", s.ListenPort, x.ID)
		}
	}
	c.Socks5Servers = append(c.Socks5Servers, s)
	return nil
}

func (c *Config) UpdateSocks5(id string, updated *Socks5Server) error {
	updated.ID = id
	if err := updated.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, x := range c.Socks5Servers {
		if x.ID != id {
			continue
		}
		// port-conflict check across all (excluding self)
		for _, m := range c.Mappings {
			if m.Protocol == "tcp" && m.ListenPort == updated.ListenPort {
				return fmt.Errorf("listen_port %d/tcp already used by mapping %q", updated.ListenPort, m.ID)
			}
		}
		for j, y := range c.Socks5Servers {
			if i == j {
				continue
			}
			if y.ListenPort == updated.ListenPort {
				return fmt.Errorf("listen_port %d/tcp already used by socks5 %q", updated.ListenPort, y.ID)
			}
		}
		updated.CreatedAt = x.CreatedAt
		c.Socks5Servers[i] = updated
		return nil
	}
	return fmt.Errorf("socks5 %q not found", id)
}

func (c *Config) DeleteSocks5(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, x := range c.Socks5Servers {
		if x.ID == id {
			c.Socks5Servers = append(c.Socks5Servers[:i], c.Socks5Servers[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("socks5 %q not found", id)
}
