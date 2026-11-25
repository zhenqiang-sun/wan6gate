package socks5

import (
	"context"
	"log/slog"
	"sync"

	"wan6gate/internal/banlist"
	"wan6gate/internal/config"
)

type runner struct {
	server *config.Socks5Server
	cancel context.CancelFunc
}

// Manager owns running SOCKS5 listeners. Independent of forwarder.Manager.
type Manager struct {
	mu      sync.Mutex
	log     *slog.Logger
	bans    *banlist.BanList
	runners map[string]*runner
}

// NewManager — bans may be nil to disable rate limiting.
func NewManager(log *slog.Logger, bans *banlist.BanList) *Manager {
	return &Manager{
		log:     log,
		bans:    bans,
		runners: make(map[string]*runner),
	}
}

// Reconcile starts/stops listeners to match enabled servers.
func (m *Manager) Reconcile(parent context.Context, servers []*config.Socks5Server) {
	m.mu.Lock()
	defer m.mu.Unlock()

	desired := make(map[string]*config.Socks5Server)
	for _, s := range servers {
		if s.Enabled {
			desired[s.ID] = s
		}
	}

	for id, r := range m.runners {
		d, ok := desired[id]
		if !ok || !sameTuple(r.server, d) {
			r.cancel()
			delete(m.runners, id)
		}
	}

	for id, s := range desired {
		if _, ok := m.runners[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(parent)
		r := &runner{server: s, cancel: cancel}
		m.runners[id] = r
		go func(s *config.Socks5Server) {
			srv, err := New(s.ID, s.ListenPort, s.Username, s.Password, m.bans, m.log.With("socks5", s.ID))
			if err != nil {
				m.log.Error("socks5 build failed", "id", s.ID, "err", err)
				return
			}
			if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
				m.log.Error("socks5 exited", "id", s.ID, "err", err)
			}
		}(s)
	}
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.runners {
		r.cancel()
		delete(m.runners, id)
	}
}

func (m *Manager) ActiveIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.runners))
	for id := range m.runners {
		out = append(out, id)
	}
	return out
}

func sameTuple(a, b *config.Socks5Server) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ListenPort == b.ListenPort &&
		a.Username == b.Username &&
		a.Password == b.Password
}
