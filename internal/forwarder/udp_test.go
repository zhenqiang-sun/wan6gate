package forwarder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wan6gate/internal/metrics"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitForPort(f *UDPForwarder, t *testing.T) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := f.listenPort.Load(); p != 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("listener port never became non-zero")
}

func listenAddrFor(f *UDPForwarder, t *testing.T) *net.UDPAddr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort("::1", strconv.Itoa(int(f.listenPort.Load()))))
	if err != nil {
		t.Fatalf("resolve listen addr: %v", err)
	}
	return addr
}

func snapFor(t *testing.T, id string) metrics.Snapshot {
	t.Helper()
	return metrics.Default.Get(id).Snapshot()
}

func TestUDPEchoAndMetrics(t *testing.T) {
	runID := t.Name() + "-" + fmt.Sprint(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Local mock UDP backend echo server.
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mock backend: %v", err)
	}
	defer ln.Close()

	targetAddr := ln.LocalAddr().(*net.UDPAddr)
	target := net.JoinHostPort(targetAddr.IP.String(), strconv.Itoa(targetAddr.Port))
	ip, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)

	f := NewUDP(runID, 0, ip, port, testLogger())
	f.idleTimeout = 250 * time.Millisecond

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			ln.WriteTo(buf[:n], addr) // echo
		}
	}()

	runErr := make(chan error, 1)
	go func() {
		runErr <- f.Run(ctx)
	}()
	waitForPort(f, t)

	listenAddr := listenAddrFor(f, t)
	client, err := net.DialUDP("udp", nil, listenAddr)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer client.Close()

	msg := []byte("hello udp")
	if _, err := client.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read echo back.
	rbuf := make([]byte, 64*1024)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(rbuf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(rbuf[:n]) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", rbuf[:n], msg)
	}

	// Metrics checks during active session.
	snap := snapFor(t, runID)
	if snap.Accepted != 1 {
		t.Errorf("Accepted=%d want 1", snap.Accepted)
	}
	if snap.Active != 1 {
		t.Errorf("Active=%d want 1 (during active session)", snap.Active)
	}
	if snap.BytesIn < int64(len(msg)) {
		t.Errorf("BytesIn=%d want >= %d", snap.BytesIn, len(msg))
	}
	if snap.BytesOut < int64(len(msg)) {
		t.Errorf("BytesOut=%d want >= %d", snap.BytesOut, len(msg))
	}

	// Wait for idle-timeout cleanup.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if snap := snapFor(t, runID); snap.Active == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap = snapFor(t, runID)
	if snap.Active != 0 {
		t.Errorf("Active=%d want 0 after idle cleanup", snap.Active)
	}

	// Send again: should create a new flow (Accepted=2) and still work.
	if _, err := client.Write(msg); err != nil {
		t.Fatalf("second write: %v", err)
	}
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = client.Read(rbuf)
	if err != nil {
		t.Fatalf("second read echo: %v", err)
	}
	if string(rbuf[:n]) != string(msg) {
		t.Fatalf("second echo mismatch: got %q want %q", rbuf[:n], msg)
	}

	snap = snapFor(t, runID)
	if snap.Accepted != 2 {
		t.Errorf("Accepted=%d want 2 after re-connect", snap.Accepted)
	}
	if snap.Active != 1 {
		t.Errorf("Active=%d want 1 during second session", snap.Active)
	}

	cancel()

	// Verify Run returns context.Canceled
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}

	// Verify flows are cleaned up
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		empty := len(f.flows) == 0
		f.mu.Unlock()
		if empty {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	if len(f.flows) != 0 {
		t.Errorf("flows not empty after cancel: %d", len(f.flows))
	}
	f.mu.Unlock()
}

func TestUDPUnidirectionalKeepalive(t *testing.T) {
	runID := t.Name() + "-" + fmt.Sprint(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mock backend: %v", err)
	}
	defer ln.Close()

	targetAddr := ln.LocalAddr().(*net.UDPAddr)
	target := net.JoinHostPort(targetAddr.IP.String(), strconv.Itoa(targetAddr.Port))
	ip, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)

	f := NewUDP(runID, 0, ip, port, testLogger())
	f.idleTimeout = 150 * time.Millisecond

	// Backend that stays silent (no reply) during the client keepalive phase,
	// then replies once after.
	readyToReply := make(chan struct{})
	go func() {
		buf := make([]byte, 64*1024)
		replied := false
		for {
			_, addr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			if !replied {
				replied = true
				// Wait for client to finish sending keepalive packets
				<-readyToReply
				// Now reply to verify flow survived the read timeout
				ln.WriteTo([]byte("pong"), addr)
			}
		}
	}()

	runErr := make(chan error, 1)
	go func() {
		runErr <- f.Run(ctx)
	}()
	waitForPort(f, t)

	listenAddr := listenAddrFor(f, t)
	client, err := net.DialUDP("udp", nil, listenAddr)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer client.Close()

	// Client sends unidirectional packets every 40ms for 420ms (> 2 * idleTimeout),
	// while backend stays silent.
	msg := []byte("ping")
	deadline := time.Now().Add(420 * time.Millisecond)
	for time.Now().Before(deadline) {
		client.Write(msg)
		time.Sleep(40 * time.Millisecond)
	}
	// All keepalive packets sent, now allow backend to reply
	close(readyToReply)

	// Wait for Accepted == 1
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap := snapFor(t, runID); snap.Accepted == 1 && snap.Active == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := snapFor(t, runID)
	if snap.Accepted != 1 {
		t.Errorf("Accepted=%d want 1 (flow should not be closed during active keepalive)", snap.Accepted)
	}
	if snap.Active != 1 {
		t.Errorf("Active=%d want 1", snap.Active)
	}

	// Now read backend reply (pong) — flow must still be alive.
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	rbuf := make([]byte, 64*1024)
	n, err := client.Read(rbuf)
	if err != nil {
		t.Fatalf("read backend reply after keepalive: %v (flow was likely closed)", err)
	}
	if string(rbuf[:n]) != "pong" {
		t.Fatalf("unexpected reply: %q", rbuf[:n])
	}

	// Verify flow still active after receiving backend reply
	snap = snapFor(t, runID)
	if snap.Accepted != 1 {
		t.Errorf("Accepted=%d want 1 after backend reply", snap.Accepted)
	}
	if snap.Active != 1 {
		t.Errorf("Active=%d want 1 after backend reply", snap.Active)
	}

	cancel()

	// Verify Run returns context.Canceled
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestUDPMetricsNoLeakOnCancel(t *testing.T) {
	runID := t.Name() + "-" + fmt.Sprint(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())

	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mock backend: %v", err)
	}
	defer ln.Close()

	targetAddr := ln.LocalAddr().(*net.UDPAddr)
	target := net.JoinHostPort(targetAddr.IP.String(), strconv.Itoa(targetAddr.Port))
	ip, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)

	f := NewUDP(runID, 0, ip, port, testLogger())
	f.idleTimeout = 250 * time.Millisecond

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			ln.WriteTo(buf[:n], addr)
		}
	}()

	runErr := make(chan error, 1)
	go func() {
		runErr <- f.Run(ctx)
	}()
	waitForPort(f, t)

	listenAddr := listenAddrFor(f, t)
	client, err := net.DialUDP("udp", nil, listenAddr)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer client.Close()

	msg := []byte("hello")
	if _, err := client.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Record test start time for LastActive validation
	testStart := time.Now().Truncate(time.Second)

	// Verify active session established.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := snapFor(t, runID)
		if snap.Active == 1 && snap.Accepted == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := snapFor(t, runID)
	if snap.Active != 1 {
		t.Errorf("Active=%d want 1 (during active session)", snap.Active)
	}
	if snap.Accepted != 1 {
		t.Errorf("Accepted=%d want 1", snap.Accepted)
	}
	if snap.LastActive == "" {
		t.Errorf("LastActive is empty")
	}
	if parsed, err := time.Parse(time.RFC3339, snap.LastActive); err != nil {
		t.Errorf("LastActive=%q not RFC3339: %v", snap.LastActive, err)
	} else if parsed.Before(testStart) || parsed.After(time.Now().Add(2*time.Second)) {
		t.Errorf("LastActive=%v not in range [%v, %v]", parsed, testStart, time.Now().Add(2*time.Second))
	}

	cancel()

	// Wait for flows cleaned up
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		empty := len(f.flows) == 0
		f.mu.Unlock()
		snap := snapFor(t, runID)
		if snap.Active == 0 && empty {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	snap = snapFor(t, runID)
	if snap.Active != 0 {
		t.Errorf("Active=%d want 0 after cancel (leak)", snap.Active)
	}
	if snap.Errors != 0 {
		t.Errorf("Errors=%d want 0", snap.Errors)
	}
	if snap.LastActive == "" {
		t.Errorf("LastActive should not be empty")
	}

	// Verify Run returns context.Canceled
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}

	f.mu.Lock()
	if len(f.flows) != 0 {
		t.Errorf("flows not empty: %d", len(f.flows))
	}
	f.mu.Unlock()
}

func TestUDPConcurrentFlowsAndReadError(t *testing.T) {
	runID := t.Name() + "-" + fmt.Sprint(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mock backend: %v", err)
	}
	defer ln.Close()

	targetAddr := ln.LocalAddr().(*net.UDPAddr)
	target := net.JoinHostPort(targetAddr.IP.String(), strconv.Itoa(targetAddr.Port))
	ip, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)

	f := NewUDP(runID, 0, ip, port, testLogger())
	f.idleTimeout = 2 * time.Second

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			ln.WriteTo(buf[:n], addr) // echo
		}
	}()

	runErr := make(chan error, 1)
	go func() {
		runErr <- f.Run(ctx)
	}()
	waitForPort(f, t)

	listenAddr := listenAddrFor(f, t)

	// 3 concurrent clients, each sending multiple packets
	const numClients = 3
	const msgsPerClient = 5
	msgLen := 10
	var totalBytes atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			client, err := net.DialUDP("udp", nil, listenAddr)
			if err != nil {
				t.Errorf("client %d dial: %v", idx, err)
				return
			}
			defer client.Close()
			msg := make([]byte, msgLen)
			for j := 0; j < msgsPerClient; j++ {
				msg[0] = byte(idx)
				msg[1] = byte(j)
				if _, err := client.Write(msg); err != nil {
					t.Errorf("client %d write %d: %v", idx, j, err)
					return
				}
				totalBytes.Add(int64(msgLen))
			}
			// Read echoes back
			rbuf := make([]byte, 64*1024)
			client.SetReadDeadline(time.Now().Add(3 * time.Second))
			for j := 0; j < msgsPerClient; j++ {
				if _, err := client.Read(rbuf); err != nil {
					t.Errorf("client %d read echo %d: %v", idx, j, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// Verify Accepted == 3
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap := snapFor(t, runID); snap.Accepted >= numClients {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := snapFor(t, runID)
	if snap.Accepted != numClients {
		t.Errorf("Accepted=%d want %d", snap.Accepted, numClients)
	}
	expectedBytes := int64(numClients * msgsPerClient * msgLen)
	if snap.BytesIn < expectedBytes {
		t.Errorf("BytesIn=%d want >= %d", snap.BytesIn, expectedBytes)
	}

	// Dial failure (invalid target IP) should not leak Active, should record Errors.
	badF := NewUDP(runID+"-bad", 0, "invalid-ip", port, testLogger())
	badF.idleTimeout = 100 * time.Millisecond
	badRunErr := make(chan error, 1)
	go func() {
		badRunErr <- badF.Run(ctx)
	}()
	waitForPort(badF, t)
	badAddr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort("::1", strconv.Itoa(int(badF.listenPort.Load()))))
	badClient, err := net.DialUDP("udp", nil, badAddr)
	if err != nil {
		t.Fatalf("dial bad forwarder: %v", err)
	}
	defer badClient.Close()
	if _, err := badClient.Write([]byte("x")); err != nil {
		t.Fatalf("bad write: %v", err)
	}

	// Wait for Accepted == 1, Errors == 1, Active == 0
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		badSnap := snapFor(t, runID+"-bad")
		if badSnap.Accepted == 1 && badSnap.Errors == 1 && badSnap.Active == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	badSnap := snapFor(t, runID+"-bad")
	if badSnap.Active != 0 {
		t.Errorf("bad forwarder Active=%d want 0 (no leak)", badSnap.Active)
	}
	if badSnap.Errors != 1 {
		t.Errorf("bad forwarder Errors=%d want 1", badSnap.Errors)
	}
	if badSnap.Accepted != 1 {
		t.Errorf("bad forwarder Accepted=%d want 1", badSnap.Accepted)
	}

	cancel()

	// Verify Run returns context.Canceled
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}

	// Verify bad forwarder Run returns context.Canceled
	select {
	case err := <-badRunErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bad Run returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bad Run did not return")
	}

	// Verify bad forwarder cleaned up too
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		badF.mu.Lock()
		empty := len(badF.flows) == 0
		badF.mu.Unlock()
		if empty {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	badF.mu.Lock()
	if len(badF.flows) != 0 {
		t.Errorf("bad flows not empty: %d", len(badF.flows))
	}
	badF.mu.Unlock()
}
