package ddns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"wan6gate/internal/config"
	"wan6gate/internal/notify"
	"wan6gate/internal/system"
)

// Manager runs the periodic DDNS sync goroutine.
type Manager struct {
	cfg     *config.Config
	cfgPath string
	log     *slog.Logger
	notif   *notify.Notifier

	mu        sync.Mutex
	triggerCh chan struct{}
}

func NewManager(cfg *config.Config, cfgPath string, log *slog.Logger, notif *notify.Notifier) *Manager {
	return &Manager{
		cfg:       cfg,
		cfgPath:   cfgPath,
		log:       log.With("module", "ddns"),
		notif:     notif,
		triggerCh: make(chan struct{}, 1),
	}
}

// Run blocks until ctx is cancelled. Sleeps according to interval_seconds,
// triggers an immediate sync on Trigger().
func (m *Manager) Run(ctx context.Context) {
	// Small delay so the rest of the daemon (UI, forwarders) is fully up first.
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return
	}
	m.SyncOnce(ctx)

	for {
		interval := time.Duration(m.cfg.GetDDNS().IntervalSeconds) * time.Second
		if interval < 60*time.Second {
			interval = 60 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-m.triggerCh:
			if !timer.Stop() {
				<-timer.C
			}
		}
		m.SyncOnce(ctx)
	}
}

// Trigger requests an immediate sync (non-blocking; coalesces).
func (m *Manager) Trigger() {
	select {
	case m.triggerCh <- struct{}{}:
	default:
	}
}

// SyncOnce performs one sync pass. Safe to call concurrently — protected by m.mu.
// Returns any error encountered. Errors are also persisted to config.LastError.
func (m *Manager) SyncOnce(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	d := m.cfg.GetDDNS()
	if !d.Enabled {
		return nil
	}
	if d.AccessKeyID == "" || d.AccessKeySecret == "" {
		m.recordErr("credentials not set")
		return errors.New("credentials not set")
	}
	if d.Domain == "" {
		m.recordErr("domain not set")
		return errors.New("domain not set")
	}

	cur, err := system.RouterIPv6()
	if err != nil || cur == "" {
		msg := "no global IPv6 found on pppoe-wan"
		if err != nil {
			msg = err.Error()
		}
		m.recordErr(msg)
		return errors.New(msg)
	}

	zone, rr, err := SplitDomain(d.Domain)
	if err != nil {
		m.recordErr(err.Error())
		return err
	}

	client := NewAliyunClient(d.AccessKeyID, d.AccessKeySecret)
	recordID := d.RecordID

	if recordID == "" {
		// First-time bootstrap: see if a record already exists
		records, err := client.DescribeDomainRecords(zone, rr, "AAAA")
		if err != nil {
			m.recordErr(err.Error())
			return err
		}
		if len(records) > 0 {
			recordID = records[0].RecordID
			if records[0].Value == cur {
				m.recordOK(cur, recordID)
				return nil
			}
		}
	}

	if recordID == "" {
		newID, err := client.AddDomainRecord(zone, rr, "AAAA", cur, d.TTL)
		if err != nil {
			m.recordErr(err.Error())
			return err
		}
		m.log.Info("ddns record created", "domain", d.Domain, "ip", cur, "record_id", newID)
		m.recordOK(cur, newID)
		m.notifyChange(d.Domain, cur, "")
		return nil
	}

	// IP unchanged: skip the Aliyun update API call but still refresh
	// LastSyncedAt locally so UI consumers can tell that the sync loop is
	// alive (otherwise a quiet WAN keeps the timestamp frozen at first sync).
	if d.LastSyncedIP == cur {
		m.recordOK(cur, recordID)
		return nil
	}

	if err := client.UpdateDomainRecord(recordID, rr, "AAAA", cur, d.TTL); err != nil {
		// If record vanished externally (e.g. user deleted it), drop the cached
		// RecordId and let the next pass re-bootstrap.
		if strings.Contains(err.Error(), "DomainRecordNotBelongToUser") ||
			strings.Contains(err.Error(), "InvalidRR.NoExist") ||
			strings.Contains(err.Error(), "RecordIdNotMatch") {
			m.cfg.SetDDNSStatus("", "-", err.Error(), nowRFC())
			m.cfg.Save(m.cfgPath)
			m.log.Warn("ddns record id stale; will re-bootstrap next pass", "err", err)
			return err
		}
		m.recordErr(err.Error())
		return err
	}
	m.log.Info("ddns record updated", "domain", d.Domain, "ip", cur, "from", d.LastSyncedIP)
	m.recordOK(cur, recordID)
	m.notifyChange(d.Domain, cur, d.LastSyncedIP)
	return nil
}

func (m *Manager) notifyChange(domain, newIP, oldIP string) {
	if m.notif == nil {
		return
	}
	detail := fmt.Sprintf("%s → %s", domain, newIP)
	if oldIP != "" && oldIP != newIP {
		detail = fmt.Sprintf("%s\n  was: %s\n  now: %s", domain, oldIP, newIP)
	}
	m.notif.Send(notify.Event{
		Type:   notify.EventDDNSChange,
		Title:  "DDNS updated",
		Detail: detail,
	})
}

func (m *Manager) recordOK(ip, recordID string) {
	if recordID == "-" {
		recordID = ""
	}
	m.cfg.SetDDNSStatus(ip, recordID, "", nowRFC())
	if err := m.cfg.Save(m.cfgPath); err != nil {
		m.log.Warn("save config", "err", err)
	}
}

func (m *Manager) recordErr(msg string) {
	m.cfg.SetDDNSStatus("", "", msg, nowRFC())
	if err := m.cfg.Save(m.cfgPath); err != nil {
		m.log.Warn("save config", "err", err)
	}
	m.log.Warn("ddns sync failed", "err", msg)
	if m.notif != nil {
		m.notif.SendDDNSErrorOnce(msg)
	}
}

func nowRFC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// SplitDomain takes a fully-qualified domain like "host.sub.example.com"
// and returns ("example.com", "host.sub") — i.e. the last 2 dot-separated
// components are treated as the zone, everything before is the RR.
//
// This works for second-level domains like ".com" / ".cn" / ".net". It does
// NOT correctly handle ccSLDs like ".co.uk" or ".com.cn"; if you need those,
// the caller must pass zone/rr separately.
func SplitDomain(fqdn string) (zone, rr string, err error) {
	fqdn = strings.TrimSuffix(strings.TrimSpace(fqdn), ".")
	if fqdn == "" {
		return "", "", errors.New("empty domain")
	}
	parts := strings.Split(fqdn, ".")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("not a fqdn: %q", fqdn)
	}
	if len(parts) == 2 {
		return fqdn, "@", nil
	}
	zone = parts[len(parts)-2] + "." + parts[len(parts)-1]
	rr = strings.Join(parts[:len(parts)-2], ".")
	return zone, rr, nil
}
