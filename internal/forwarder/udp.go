package forwarder

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"wan6gate/internal/metrics"
)

const (
	udpFlowIdle = 60 * time.Second
	udpBufSize  = 64 * 1024
)

// UDPForwarder is a stateful UDP NAT-style forwarder.
// One listening socket on [::]:listenPort; one backend socket per remote client.
type UDPForwarder struct {
	id         string
	listenPort atomic.Int32
	targetIP   string
	targetPort int
	log        *slog.Logger

	idleTimeout time.Duration

	mu     sync.Mutex
	flows  map[string]*udpFlow
	closed bool // protected by mu

	wg       sync.WaitGroup
	monoBase time.Time // monotonic time base for idle calculations
}

type udpFlow struct {
	key      string
	backend  *net.UDPConn
	client   net.Addr
	lastSeen atomic.Int64 // monotonic nanoseconds

	counters *metrics.Counters
	end      func(error)

	closeOnce sync.Once
	closed    atomic.Bool // true after closeFlow begins
}

func NewUDP(id string, listenPort int, targetIP string, targetPort int, log *slog.Logger) *UDPForwarder {
	now := time.Now()
	f := &UDPForwarder{
		id:         id,
		targetIP:   targetIP,
		targetPort: targetPort,
		log: log.With(
			"proto", "udp",
			"listen", listenPort,
			"target", targetIP+":"+strconv.Itoa(targetPort),
		),
		idleTimeout: udpFlowIdle,
		flows:       make(map[string]*udpFlow),
		monoBase:    now,
	}
	f.listenPort.Store(int32(listenPort))
	return f
}

func (f *UDPForwarder) idle() time.Duration {
	if f.idleTimeout > 0 {
		return f.idleTimeout
	}
	return udpFlowIdle
}

// nowMonoNanos returns current monotonic time in nanoseconds since f.monoBase.
// This is immune to system clock adjustments.
func (f *UDPForwarder) nowMonoNanos() int64 {
	return time.Since(f.monoBase).Nanoseconds()
}

// shutdown performs unified cleanup: closes pc, marks closed, snapshots flows,
// and closes each flow. Called once when runCtx is cancelled.
func (f *UDPForwarder) shutdown(pc net.PacketConn) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	flows := make([]*udpFlow, 0, len(f.flows))
	for _, fl := range f.flows {
		flows = append(flows, fl)
	}
	f.mu.Unlock()

	pc.Close()
	for _, fl := range flows {
		f.closeFlow(fl, nil)
	}
}

func (f *UDPForwarder) Run(ctx context.Context) error {
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var lc net.ListenConfig
	pc, err := lc.ListenPacket(runCtx, "udp6", "[::]:"+strconv.Itoa(int(f.listenPort.Load())))
	if err != nil {
		f.log.Error("listen failed", "err", err)
		return err
	}
	if f.listenPort.Load() == 0 {
		if ua, ok := pc.LocalAddr().(*net.UDPAddr); ok {
			f.listenPort.Store(int32(ua.Port))
		}
	}
	f.log.Info("udp forwarder listening")

	// Unblock pc.ReadFrom when runCtx is cancelled (external cancel or our runCancel).
	// This goroutine always fires because the defer below always calls runCancel().
	go func() {
		<-runCtx.Done()
		pc.Close()
	}()

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		f.janitor(runCtx)
	}()

	// Ensure shutdown runs on any exit path.
	// Ordering matters:
	//   - runCancel() signals runCtx done -> the pc.Close goroutine unblocks ReadFrom,
	//     and janitor() returns.
	//   - f.shutdown(pc) marks f.closed (so getOrCreateFlow refuses new flows),
	//     snapshots remaining flows and closes them (their backend goroutines exit).
	//   - f.wg.Wait() waits for janitor + backend goroutines to finish.
	defer func() {
		runCancel()
		f.shutdown(pc)
		f.wg.Wait()
	}()

	buf := make([]byte, udpBufSize)
	for {
		n, clientAddr, err := pc.ReadFrom(buf)
		if err != nil {
			if runCtx.Err() != nil {
				// runCtx cancelled (either external ctx or our runCancel)
				// shutdown will be called by defer
				return runCtx.Err()
			}
			f.log.Warn("read error", "err", err)
			// Trigger shutdown on unexpected read error
			runCancel()
			return err
		}
		fl := f.getOrCreateFlow(runCtx, pc, clientAddr)
		if fl == nil {
			continue
		}
		written, werr := fl.backend.Write(buf[:n])
		if werr != nil {
			// If flow or runCtx is already closed, this is expected cleanup, not a business error
			if fl.closed.Load() || runCtx.Err() != nil {
				continue
			}
			f.log.Warn("backend write failed", "client", clientAddr, "err", werr)
			atomic.AddInt64(&fl.counters.Errors, 1)
			continue
		}
		if written > 0 {
			atomic.AddInt64(&fl.counters.BytesIn, int64(written))
			atomic.StoreInt64(&fl.counters.LastActive, time.Now().UnixNano())
		}
		// Update lastSeen under lock in getOrCreateFlow; here just store the monotonic time
		fl.lastSeen.Store(f.nowMonoNanos())
	}
}

func (f *UDPForwarder) getOrCreateFlow(ctx context.Context, pc net.PacketConn, clientAddr net.Addr) *udpFlow {
	key := clientAddr.String()
	f.mu.Lock()
	// Check closed flag and context cancellation while holding the lock
	// This prevents the race where a new flow is created after janitor snapshots flows
	if f.closed || ctx.Err() != nil {
		f.mu.Unlock()
		return nil
	}
	if fl, ok := f.flows[key]; ok {
		// Update lastSeen while still holding the lock to avoid race with janitor
		fl.lastSeen.Store(f.nowMonoNanos())
		f.mu.Unlock()
		return fl
	}
	ip := net.ParseIP(f.targetIP)
	if ip == nil {
		f.mu.Unlock()
		f.log.Warn("invalid target ip", "client", clientAddr, "target", f.targetIP)
		counters, end := metrics.Begin(f.id)
		end(fmt.Errorf("invalid target ip: %q", f.targetIP))
		_ = counters
		return nil
	}
	target := &net.UDPAddr{IP: ip, Port: f.targetPort}
	backend, err := net.DialUDP("udp", nil, target)
	if err != nil {
		f.mu.Unlock()
		f.log.Warn("dial backend failed", "client", clientAddr, "err", err)
		counters, end := metrics.Begin(f.id)
		end(err)
		_ = counters
		return nil
	}
	// Thoroughly close the cancel window: after DialUDP, before metrics.Begin and
	// storing the flow, re-check closed/ctx while still holding f.mu. If shutdown
	// happened in the meantime, drop the backend without touching metrics so no
	// phantom Accepted/Active counters are inflated.
	if f.closed || ctx.Err() != nil {
		backend.Close()
		f.mu.Unlock()
		return nil
	}
	counters, end := metrics.Begin(f.id)
	fl := &udpFlow{
		key:      key,
		backend:  backend,
		client:   clientAddr,
		counters: counters,
		end:      end,
	}
	fl.lastSeen.Store(f.nowMonoNanos())
	atomic.StoreInt64(&fl.counters.LastActive, time.Now().UnixNano())
	f.flows[key] = fl
	f.mu.Unlock()

	f.log.Debug("udp flow created", "client", clientAddr)

	// Backend → client return path
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		rbuf := make([]byte, udpBufSize)
		for {
			backend.SetReadDeadline(time.Now().Add(f.idle()))
			n, err := backend.Read(rbuf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					// Unified idle-close path: hold f.mu, re-check presence and
					// monotonic lastSeen, atomically remove and close. If it
					// returns false, the flow was just refreshed by client or
					// backend traffic, so keep reading.
					if !f.closeFlowIfIdle(fl) {
						continue
					}
					return
				} else {
					if ctx.Err() == nil {
						f.log.Warn("backend read failed", "client", clientAddr, "err", err)
					}
					// ctx cancelled => normal shutdown, not a business error
					if ctx.Err() != nil {
						f.closeFlow(fl, nil)
					} else {
						f.closeFlow(fl, err)
					}
				}
				return
			}
			if ctx.Err() != nil {
				f.closeFlow(fl, nil)
				return
			}
			f.mu.Lock()
			if cur, ok := f.flows[fl.key]; ok && cur == fl && !f.closed {
				fl.lastSeen.Store(f.nowMonoNanos())
			}
			f.mu.Unlock()
			written, werr := pc.WriteTo(rbuf[:n], clientAddr)
			if werr != nil {
				// If flow or runCtx is already closed, this is expected cleanup, not a business error
				if fl.closed.Load() || ctx.Err() != nil {
					f.closeFlow(fl, nil)
					return
				}
				f.log.Warn("client write failed", "client", clientAddr, "err", werr)
				f.closeFlow(fl, werr)
				return
			}
			if written > 0 {
				atomic.AddInt64(&fl.counters.BytesOut, int64(written))
				atomic.StoreInt64(&fl.counters.LastActive, time.Now().UnixNano())
			}
			fl.lastSeen.Store(f.nowMonoNanos())
		}
	}()
	return fl
}

// closeFlowIfIdle atomically checks (under f.mu) that fl is still the current
// flow for its key and has been idle for >= idle(), removes it from the map,
// then closes it. Returns true if closed, false if the flow was just refreshed
// or already removed/replaced. Used by both the backend read goroutine on
// timeout and the janitor so they share exactly the same lock-checked removal.
func (f *UDPForwarder) closeFlowIfIdle(fl *udpFlow) bool {
	f.mu.Lock()
	if cur, ok := f.flows[fl.key]; !ok || cur != fl {
		f.mu.Unlock()
		return false
	}
	if f.nowMonoNanos()-fl.lastSeen.Load() < f.idle().Nanoseconds() {
		f.mu.Unlock()
		return false
	}
	delete(f.flows, fl.key)
	f.mu.Unlock()

	f.closeFlow(fl, nil)
	return true
}

// closeFlow performs full cleanup. Called from read goroutine and janitor.
func (f *UDPForwarder) closeFlow(fl *udpFlow, err error) {
	fl.closeOnce.Do(func() {
		fl.closed.Store(true)
		fl.backend.Close()
		f.mu.Lock()
		if cur, ok := f.flows[fl.key]; ok && cur == fl {
			delete(f.flows, fl.key)
		}
		f.mu.Unlock()
		if fl.end != nil {
			fl.end(err)
		}
		if err != nil {
			f.log.Debug("udp flow closed", "client", fl.client, "err", err)
		} else {
			f.log.Debug("udp flow closed", "client", fl.client)
		}
	})
}

func (f *UDPForwarder) janitor(ctx context.Context) {
	t := time.NewTicker(f.idle() / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Snapshot candidates under the lock, then use closeFlowIfIdle per
			// flow so the janitor goes through the exact same held-lock
			// re-validation + atomic removal as the backend read goroutine.
			// This closes the window between releasing the lock and closing:
			// closeFlowIfIdle will refuse to close a flow that was refreshed.
			f.mu.Lock()
			candidates := make([]*udpFlow, 0, len(f.flows))
			for _, fl := range f.flows {
				candidates = append(candidates, fl)
			}
			f.mu.Unlock()
			for _, fl := range candidates {
				if f.closeFlowIfIdle(fl) {
					f.log.Debug("udp flow reaped", "client", fl.client)
				}
			}
		}
	}
}
