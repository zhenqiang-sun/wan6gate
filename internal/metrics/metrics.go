// Package metrics holds in-memory atomic counters for forward + socks5
// services. Reset on daemon restart (no persistence by design).
package metrics

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Counters captures live state for a single service (a forward mapping or a
// socks5 server). All counters are monotonic except Active (gauge).
type Counters struct {
	Active     int64 // current open connections (gauge)
	Accepted   int64 // total accepted connections (counter)
	Errors     int64 // dial/auth/io errors (counter)
	BytesIn    int64 // client → server (counter)
	BytesOut   int64 // server → client (counter)
	LastActive int64 // unix nanos of most recent traffic
}

// Snapshot is a JSON-friendly point-in-time copy of Counters.
type Snapshot struct {
	Active     int64  `json:"active"`
	Accepted   int64  `json:"accepted"`
	Errors     int64  `json:"errors"`
	BytesIn    int64  `json:"bytes_in"`
	BytesOut   int64  `json:"bytes_out"`
	LastActive string `json:"last_active,omitempty"`
}

func (c *Counters) Snapshot() Snapshot {
	la := atomic.LoadInt64(&c.LastActive)
	var laStr string
	if la > 0 {
		laStr = time.Unix(0, la).UTC().Format(time.RFC3339)
	}
	return Snapshot{
		Active:     atomic.LoadInt64(&c.Active),
		Accepted:   atomic.LoadInt64(&c.Accepted),
		Errors:     atomic.LoadInt64(&c.Errors),
		BytesIn:    atomic.LoadInt64(&c.BytesIn),
		BytesOut:   atomic.LoadInt64(&c.BytesOut),
		LastActive: laStr,
	}
}

// Registry holds Counters keyed by service ID. Lookups create on demand.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counters
}

func NewRegistry() *Registry {
	return &Registry{counters: make(map[string]*Counters)}
}

// Get returns (creates if missing) the Counters for id.
func (r *Registry) Get(id string) *Counters {
	r.mu.RLock()
	c, ok := r.counters[id]
	r.mu.RUnlock()
	if ok {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[id]; ok {
		return c
	}
	c = &Counters{}
	r.counters[id] = c
	return c
}

// SnapshotAll returns a copy of all counters keyed by service ID.
func (r *Registry) SnapshotAll() map[string]Snapshot {
	r.mu.RLock()
	out := make(map[string]Snapshot, len(r.counters))
	for k, v := range r.counters {
		out[k] = v.Snapshot()
	}
	r.mu.RUnlock()
	return out
}

// Drop removes a counter (use when a service is permanently deleted).
func (r *Registry) Drop(id string) {
	r.mu.Lock()
	delete(r.counters, id)
	r.mu.Unlock()
}

// Default is the package-level singleton.
var Default = NewRegistry()

// --- Connection-lifecycle helpers ---

// Begin marks a new accepted connection: Accepted++ and Active++.
// Returns a closer to call when the connection ends.
func Begin(serviceID string) (counters *Counters, end func(err error)) {
	c := Default.Get(serviceID)
	atomic.AddInt64(&c.Accepted, 1)
	atomic.AddInt64(&c.Active, 1)
	atomic.StoreInt64(&c.LastActive, time.Now().UnixNano())
	return c, func(err error) {
		atomic.AddInt64(&c.Active, -1)
		if err != nil {
			atomic.AddInt64(&c.Errors, 1)
		}
	}
}

// CountedReader wraps a Reader and increments the given counter on every Read.
type CountedReader struct {
	R       io.Reader
	Counter *int64
	Last    *int64 // optional, updated to time.Now().UnixNano() on activity
}

func (cr *CountedReader) Read(p []byte) (int, error) {
	n, err := cr.R.Read(p)
	if n > 0 {
		atomic.AddInt64(cr.Counter, int64(n))
		if cr.Last != nil {
			atomic.StoreInt64(cr.Last, time.Now().UnixNano())
		}
	}
	return n, err
}

// CountedWriter wraps a Writer and increments the given counter on every Write.
// OnActivity, if non-nil, fires after every successful Write (n > 0) — used by
// proxies to refresh per-connection idle deadlines without coupling metrics to net.Conn.
type CountedWriter struct {
	W          io.Writer
	Counter    *int64
	Last       *int64
	OnActivity func()
}

func (cw *CountedWriter) Write(p []byte) (int, error) {
	n, err := cw.W.Write(p)
	if n > 0 {
		atomic.AddInt64(cw.Counter, int64(n))
		if cw.Last != nil {
			atomic.StoreInt64(cw.Last, time.Now().UnixNano())
		}
		if cw.OnActivity != nil {
			cw.OnActivity()
		}
	}
	return n, err
}
