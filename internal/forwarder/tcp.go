package forwarder

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"wan6gate/internal/metrics"
)

const (
	tcpDialTimeout = 5 * time.Second
	// tcpIdleTimeout is the max gap between traffic in either direction before
	// the connection is force-closed. Refreshed on every successful copy in
	// either direction (see handle).
	tcpIdleTimeout = 5 * time.Minute
	// tcpIdleRefreshInterval throttles SetDeadline syscalls during high-throughput
	// streams. Worst-case true-idle close happens at tcpIdleTimeout + this.
	tcpIdleRefreshInterval = 30 * time.Second
)

// TCPForwarder listens on [::]:listenPort and forwards each connection to target.
type TCPForwarder struct {
	id         string
	listenPort int
	target     string
	log        *slog.Logger

	wg sync.WaitGroup
}

// NewTCP — id is the service identifier used for metrics.
func NewTCP(id string, listenPort int, targetIP string, targetPort int, log *slog.Logger) *TCPForwarder {
	return &TCPForwarder{
		id:         id,
		listenPort: listenPort,
		target:     net.JoinHostPort(targetIP, strconv.Itoa(targetPort)),
		log: log.With(
			"proto", "tcp",
			"listen", listenPort,
			"target", targetIP+":"+strconv.Itoa(targetPort),
		),
	}
}

func (f *TCPForwarder) Run(ctx context.Context) error {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp6", "[::]:"+strconv.Itoa(f.listenPort))
	if err != nil {
		f.log.Error("listen failed", "err", err)
		return err
	}
	f.log.Info("tcp forwarder listening")

	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				f.wg.Wait()
				f.log.Info("tcp forwarder stopped")
				return ctx.Err()
			}
			f.log.Warn("accept error", "err", err)
			f.wg.Wait()
			return err
		}
		f.wg.Add(1)
		go f.handle(ctx, c)
	}
}

func (f *TCPForwarder) handle(ctx context.Context, client net.Conn) {
	defer f.wg.Done()
	defer client.Close()

	counters, end := metrics.Begin(f.id)
	var dialErr error
	defer func() { end(dialErr) }()

	dctx, cancel := context.WithTimeout(ctx, tcpDialTimeout)
	defer cancel()
	var d net.Dialer
	backend, err := d.DialContext(dctx, "tcp", f.target)
	if err != nil {
		f.log.Warn("dial backend failed", "client", client.RemoteAddr(), "err", err)
		dialErr = err
		return
	}
	defer backend.Close()

	// Throttled deadline refresh: keep both ends' deadlines rolling while
	// traffic flows, so the connection only dies after a true idle gap.
	var lastRefreshNano atomic.Int64
	refreshDeadline := func() {
		now := time.Now().UnixNano()
		last := lastRefreshNano.Load()
		if now-last < int64(tcpIdleRefreshInterval) {
			return
		}
		if !lastRefreshNano.CompareAndSwap(last, now) {
			return
		}
		d := time.Unix(0, now).Add(tcpIdleTimeout)
		client.SetDeadline(d)
		backend.SetDeadline(d)
	}
	refreshDeadline() // prime initial deadline

	f.log.Debug("connection accepted", "client", client.RemoteAddr())

	var wg sync.WaitGroup
	wg.Add(2)
	// client → backend: counts as BytesIn (from external/client perspective)
	go func() {
		defer wg.Done()
		io.Copy(&metrics.CountedWriter{W: backend, Counter: &counters.BytesIn, Last: &counters.LastActive, OnActivity: refreshDeadline}, client)
		if c, ok := backend.(*net.TCPConn); ok {
			c.CloseWrite()
		}
	}()
	// backend → client: counts as BytesOut
	go func() {
		defer wg.Done()
		io.Copy(&metrics.CountedWriter{W: client, Counter: &counters.BytesOut, Last: &counters.LastActive, OnActivity: refreshDeadline}, backend)
		if c, ok := client.(*net.TCPConn); ok {
			c.CloseWrite()
		}
	}()
	wg.Wait()
}
