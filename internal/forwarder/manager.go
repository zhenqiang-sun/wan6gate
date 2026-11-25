package forwarder

import (
	"context"
	"log/slog"
	"sync"

	"wan6gate/internal/config"
)

// Forwarder is the common interface satisfied by TCP and UDP forwarders.
type Forwarder interface {
	Run(ctx context.Context) error
}

type runner struct {
	mapping *config.Mapping
	cancel  context.CancelFunc
}

// Manager owns running TCP/UDP port forwarders. SOCKS5 lives in its own manager
// (see internal/socks5/manager.go) — these two are intentionally decoupled.
type Manager struct {
	mu      sync.Mutex
	log     *slog.Logger
	runners map[string]*runner
}

func NewManager(log *slog.Logger) *Manager {
	return &Manager{
		log:     log,
		runners: make(map[string]*runner),
	}
}

func (m *Manager) Reconcile(parent context.Context, mappings []*config.Mapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	desired := make(map[string]*config.Mapping)
	for _, mp := range mappings {
		if mp.Enabled {
			desired[mp.ID] = mp
		}
	}

	for id, r := range m.runners {
		d, ok := desired[id]
		if !ok || !sameTuple(r.mapping, d) {
			r.cancel()
			delete(m.runners, id)
		}
	}

	for id, mp := range desired {
		if _, ok := m.runners[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(parent)
		r := &runner{mapping: mp, cancel: cancel}
		m.runners[id] = r
		go func(mp *config.Mapping) {
			var fwd Forwarder
			switch mp.Protocol {
			case "tcp":
				fwd = NewTCP(mp.ID, mp.ListenPort, mp.TargetIP, mp.TargetPort, m.log.With("mapping", mp.ID))
			case "udp":
				fwd = NewUDP(mp.ID, mp.ListenPort, mp.TargetIP, mp.TargetPort, m.log.With("mapping", mp.ID))
			default:
				m.log.Error("unknown protocol", "mapping", mp.ID, "proto", mp.Protocol)
				return
			}
			if err := fwd.Run(ctx); err != nil && ctx.Err() == nil {
				m.log.Error("forwarder exited", "mapping", mp.ID, "err", err)
			}
		}(mp)
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

func sameTuple(a, b *config.Mapping) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Protocol == b.Protocol &&
		a.ListenPort == b.ListenPort &&
		a.TargetIP == b.TargetIP &&
		a.TargetPort == b.TargetPort
}
