// Package banlist implements a tiny in-memory rate-limit + ban table
// for SOCKS5 auth abuse protection.
//
// Policy: thresholdFails failures within failWindow per IP → ban for banDuration.
// Reset on daemon restart (in-memory only).
package banlist

import (
	"sync"
	"time"
)

const (
	thresholdFails = 10              // fails within window to trigger ban
	failWindow     = 5 * time.Minute // sliding window for fail count
	banDuration    = 30 * time.Minute
	gcEvery        = 5 * time.Minute
)

// BanList tracks recent auth failures and active bans.
type BanList struct {
	mu          sync.Mutex
	fails       map[string][]time.Time // ip → recent fail timestamps
	bannedUntil map[string]time.Time   // ip → unban time
	stopGC      chan struct{}
	onBan       func(ip string) // optional, fired async on new ban
}

// Entry is the per-IP view exposed to the UI / API.
type Entry struct {
	IP         string `json:"ip"`
	Fails      int    `json:"fails"`
	LastFailAt string `json:"last_fail_at,omitempty"`
	BannedAt   string `json:"banned_at,omitempty"`
	UnbansAt   string `json:"unbans_at,omitempty"`
	IsBanned   bool   `json:"is_banned"`
}

func New() *BanList {
	b := &BanList{
		fails:       make(map[string][]time.Time),
		bannedUntil: make(map[string]time.Time),
		stopGC:      make(chan struct{}),
	}
	go b.gcLoop()
	return b
}

// SetOnBan registers a callback fired (in a fresh goroutine) whenever a
// previously-unbanned IP transitions to banned via RecordFail. Pass nil to clear.
// Safe to call once at wiring time; not safe to call concurrently with RecordFail.
func (b *BanList) SetOnBan(fn func(ip string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onBan = fn
}

// IsBanned reports whether ip is currently banned.
// Hot-path: called on every TCP accept, so cheap.
func (b *BanList) IsBanned(ip string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.bannedUntil[ip]
	if !ok {
		return false
	}
	if time.Now().After(t) {
		delete(b.bannedUntil, ip)
		return false
	}
	return true
}

// RecordFail logs an auth failure for ip. If recent fails ≥ thresholdFails
// within failWindow, bans the IP for banDuration.
func (b *BanList) RecordFail(ip string) (banned bool) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := now.Add(-failWindow)
	old := b.fails[ip]
	pruned := old[:0]
	for _, t := range old {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	pruned = append(pruned, now)
	b.fails[ip] = pruned

	if len(pruned) >= thresholdFails {
		// Only fire onBan when transitioning from unbanned → banned to avoid
		// duplicate notifications if the same IP keeps hitting auth.
		_, already := b.bannedUntil[ip]
		b.bannedUntil[ip] = now.Add(banDuration)
		if !already && b.onBan != nil {
			fn := b.onBan
			go fn(ip)
		}
		return true
	}
	return false
}

// Unban removes ip from the ban table (admin action).
func (b *BanList) Unban(ip string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.bannedUntil, ip)
	delete(b.fails, ip)
}

// Snapshot returns a slice of all IPs we're tracking (banned or with recent fails).
func (b *BanList) Snapshot() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-failWindow)

	seen := make(map[string]bool)
	var out []Entry

	for ip, until := range b.bannedUntil {
		if now.After(until) {
			continue
		}
		e := Entry{IP: ip, IsBanned: true, UnbansAt: until.UTC().Format(time.RFC3339)}
		if fails := b.fails[ip]; len(fails) > 0 {
			e.Fails = countAfter(fails, cutoff)
			e.LastFailAt = fails[len(fails)-1].UTC().Format(time.RFC3339)
		}
		seen[ip] = true
		out = append(out, e)
	}
	for ip, fails := range b.fails {
		if seen[ip] {
			continue
		}
		recent := countAfter(fails, cutoff)
		if recent == 0 {
			continue
		}
		out = append(out, Entry{
			IP:         ip,
			Fails:      recent,
			LastFailAt: fails[len(fails)-1].UTC().Format(time.RFC3339),
		})
	}
	return out
}

// Stop terminates the GC loop. Use in tests or graceful shutdown.
func (b *BanList) Stop() { close(b.stopGC) }

func (b *BanList) gcLoop() {
	t := time.NewTicker(gcEvery)
	defer t.Stop()
	for {
		select {
		case <-b.stopGC:
			return
		case <-t.C:
			b.gc()
		}
	}
}

func (b *BanList) gc() {
	now := time.Now()
	cutoff := now.Add(-failWindow)
	b.mu.Lock()
	defer b.mu.Unlock()
	for ip, until := range b.bannedUntil {
		if now.After(until) {
			delete(b.bannedUntil, ip)
		}
	}
	for ip, fails := range b.fails {
		pruned := fails[:0]
		for _, t := range fails {
			if t.After(cutoff) {
				pruned = append(pruned, t)
			}
		}
		if len(pruned) == 0 {
			delete(b.fails, ip)
		} else {
			b.fails[ip] = pruned
		}
	}
}

func countAfter(ts []time.Time, cutoff time.Time) int {
	n := 0
	for _, t := range ts {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}
