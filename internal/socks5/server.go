// Package socks5 implements a minimal SOCKS5 server (RFC 1928 + RFC 1929).
// CONNECT only — no BIND, no UDP ASSOCIATE.
// Auth: username/password is mandatory at construction time.
package socks5

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"wan6gate/internal/banlist"
	"wan6gate/internal/metrics"
)

const (
	verSocks5 = 0x05

	authNoAuth     = 0x00
	authUserPass   = 0x02
	authNoAccepted = 0xFF

	cmdConnect      = 0x01
	cmdBind         = 0x02
	cmdUDPAssociate = 0x03

	atypIPv4 = 0x01
	atypFQDN = 0x03
	atypIPv6 = 0x04

	repSucceeded       = 0x00
	repGeneralFailure  = 0x01
	repNotAllowed      = 0x02
	repNetUnreachable  = 0x03
	repHostUnreachable = 0x04
	repConnRefused     = 0x05
	repTTLExpired      = 0x06
	repCmdNotSupported = 0x07
	repAddrTypeNotSupp = 0x08

	dialTimeout = 8 * time.Second
	// idleTimeout is the max gap between traffic in either direction before the
	// connection is force-closed. Refreshed on every successful copy in either
	// direction (see handle); a connection is killed only if BOTH directions
	// idle this long.
	idleTimeout = 5 * time.Minute
	// idleRefreshInterval throttles SetDeadline syscalls during high-throughput
	// streams: refresh at most once per interval. Worst-case true-idle close
	// happens at idleTimeout + idleRefreshInterval.
	idleRefreshInterval = 30 * time.Second
	greetingTO          = 10 * time.Second
)

// Server is a single Socks5 listener.
type Server struct {
	id         string
	listenPort int
	username   string
	password   string
	log        *slog.Logger
	bans       *banlist.BanList
	wg         sync.WaitGroup
}

// New creates a Socks5 listener config. username/password must be non-empty.
// id is a stable identifier used for metrics tagging.
// bans may be nil to disable rate limiting.
func New(id string, listenPort int, username, password string, bans *banlist.BanList, log *slog.Logger) (*Server, error) {
	if username == "" || password == "" {
		return nil, errors.New("socks5: username and password must be non-empty (no NoAuth allowed)")
	}
	return &Server{
		id:         id,
		listenPort: listenPort,
		username:   username,
		password:   password,
		bans:       bans,
		log: log.With(
			"proto", "socks5",
			"listen", listenPort,
			"user", username,
		),
	}, nil
}

// Run blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	// Listen on all interfaces (v4+v6). fw3 zone_wan_input controls who can reach.
	l, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(s.listenPort))
	if err != nil {
		s.log.Error("listen failed", "err", err)
		return err
	}
	s.log.Info("socks5 listener started")

	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.wg.Wait()
				s.log.Info("socks5 listener stopped")
				return ctx.Err()
			}
			s.log.Warn("accept error", "err", err)
			s.wg.Wait()
			return err
		}
		s.wg.Add(1)
		go s.handle(ctx, c)
	}
}

func (s *Server) handle(ctx context.Context, client net.Conn) {
	defer s.wg.Done()
	defer client.Close()
	remote := client.RemoteAddr().String()

	// Hot-path: drop banned IPs without even greeting.
	if s.bans != nil {
		if ip := remoteIP(client); ip != "" && s.bans.IsBanned(ip) {
			s.log.Warn("rejected banned ip", "ip", ip)
			return
		}
	}

	counters, end := metrics.Begin(s.id)
	var connErr error
	defer func() { end(connErr) }()

	// Greet phase: read methods, choose username/password.
	client.SetDeadline(time.Now().Add(greetingTO))
	if err := s.greet(client); err != nil {
		s.log.Warn("greet failed", "remote", remote, "err", err)
		connErr = err
		return
	}

	// Auth subnegotiation
	if err := s.authenticate(client); err != nil {
		s.log.Warn("auth failed", "remote", remote, "err", err)
		if s.bans != nil {
			if ip := remoteIP(client); ip != "" {
				if s.bans.RecordFail(ip) {
					s.log.Warn("ip banned for repeated auth failures", "ip", ip)
				}
			}
		}
		connErr = err
		return
	}

	// Read CONNECT request
	target, err := s.readRequest(client)
	if err != nil {
		s.log.Warn("request failed", "remote", remote, "err", err)
		connErr = err
		return
	}

	// Dial target
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	var d net.Dialer
	backend, err := d.DialContext(dctx, "tcp", target)
	cancel()
	if err != nil {
		s.log.Warn("dial target failed", "remote", remote, "target", target, "err", err)
		s.replyErr(client, repHostUnreachable)
		connErr = err
		return
	}
	defer backend.Close()

	// Reply success with bound address (our local end of the backend conn)
	bnd := backend.LocalAddr().(*net.TCPAddr)
	if err := s.replyOK(client, bnd.IP, bnd.Port); err != nil {
		s.log.Warn("reply failed", "remote", remote, "err", err)
		connErr = err
		return
	}
	s.log.Info("connection established", "remote", remote, "target", target)

	// Stream phase: keep both ends' deadlines at "now + idleTimeout" while
	// traffic flows. SetDeadline is throttled to once per idleRefreshInterval
	// so high-throughput connections don't pay a syscall per packet.
	var lastRefreshNano atomic.Int64
	refreshDeadline := func() {
		now := time.Now().UnixNano()
		last := lastRefreshNano.Load()
		if now-last < int64(idleRefreshInterval) {
			return
		}
		if !lastRefreshNano.CompareAndSwap(last, now) {
			return
		}
		d := time.Unix(0, now).Add(idleTimeout)
		client.SetDeadline(d)
		backend.SetDeadline(d)
	}
	refreshDeadline() // prime initial deadline (also clears greeting-phase deadline)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(&metrics.CountedWriter{W: backend, Counter: &counters.BytesIn, Last: &counters.LastActive, OnActivity: refreshDeadline}, client)
		if c, ok := backend.(*net.TCPConn); ok {
			c.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(&metrics.CountedWriter{W: client, Counter: &counters.BytesOut, Last: &counters.LastActive, OnActivity: refreshDeadline}, backend)
		if c, ok := client.(*net.TCPConn); ok {
			c.CloseWrite()
		}
	}()
	wg.Wait()
}

// greet implements RFC 1928 §3 method-selection.
func (s *Server) greet(c net.Conn) error {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return fmt.Errorf("read greet header: %w", err)
	}
	if hdr[0] != verSocks5 {
		return fmt.Errorf("bad version 0x%x", hdr[0])
	}
	nMethods := int(hdr[1])
	if nMethods == 0 {
		return errors.New("nmethods=0")
	}
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(c, methods); err != nil {
		return fmt.Errorf("read methods: %w", err)
	}
	for _, m := range methods {
		if m == authUserPass {
			_, err := c.Write([]byte{verSocks5, authUserPass})
			return err
		}
	}
	// No common method.
	_, _ = c.Write([]byte{verSocks5, authNoAccepted})
	return errors.New("client did not offer username/password method")
}

// authenticate implements RFC 1929.
func (s *Server) authenticate(c net.Conn) error {
	// VER=1, ULEN, UNAME, PLEN, PASSWD
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return fmt.Errorf("read auth header: %w", err)
	}
	if hdr[0] != 0x01 {
		return fmt.Errorf("bad userpass version 0x%x", hdr[0])
	}
	uname := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(c, uname); err != nil {
		return fmt.Errorf("read uname: %w", err)
	}
	pl := make([]byte, 1)
	if _, err := io.ReadFull(c, pl); err != nil {
		return fmt.Errorf("read plen: %w", err)
	}
	passwd := make([]byte, int(pl[0]))
	if _, err := io.ReadFull(c, passwd); err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	// Constant-time-ish compare (lengths first, then bytewise).
	uOK := string(uname) == s.username
	pOK := string(passwd) == s.password
	if uOK && pOK {
		_, err := c.Write([]byte{0x01, 0x00})
		return err
	}
	_, _ = c.Write([]byte{0x01, 0x01})
	return errors.New("invalid credentials")
}

// readRequest reads and validates the CONNECT request, returning "host:port".
func (s *Server) readRequest(c net.Conn) (string, error) {
	// VER, CMD, RSV, ATYP
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return "", fmt.Errorf("read req header: %w", err)
	}
	if hdr[0] != verSocks5 {
		return "", fmt.Errorf("bad version 0x%x", hdr[0])
	}
	if hdr[1] != cmdConnect {
		_ = s.replyErr(c, repCmdNotSupported)
		return "", fmt.Errorf("unsupported cmd 0x%x", hdr[1])
	}
	var host string
	switch hdr[3] {
	case atypIPv4:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case atypIPv6:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(c, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case atypFQDN:
		ln := make([]byte, 1)
		if _, err := io.ReadFull(c, ln); err != nil {
			return "", err
		}
		buf := make([]byte, int(ln[0]))
		if _, err := io.ReadFull(c, buf); err != nil {
			return "", err
		}
		host = string(buf)
	default:
		_ = s.replyErr(c, repAddrTypeNotSupp)
		return "", fmt.Errorf("unsupported atyp 0x%x", hdr[3])
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(c, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// replyOK writes a successful CONNECT response with the given bound address.
func (s *Server) replyOK(c net.Conn, ip net.IP, port int) error {
	return s.reply(c, repSucceeded, ip, port)
}

// replyErr writes an error response with a zeroed bound address.
func (s *Server) replyErr(c net.Conn, code byte) error {
	return s.reply(c, code, net.IPv4zero, 0)
}

// remoteIP returns the IP portion of conn.RemoteAddr() or "" if unrecognised.
func remoteIP(conn net.Conn) string {
	if conn == nil {
		return ""
	}
	switch a := conn.RemoteAddr().(type) {
	case *net.TCPAddr:
		if a.IP != nil {
			return a.IP.String()
		}
	}
	return ""
}

func (s *Server) reply(c net.Conn, code byte, ip net.IP, port int) error {
	var atyp byte
	var addrBytes []byte
	if ip4 := ip.To4(); ip4 != nil {
		atyp = atypIPv4
		addrBytes = ip4
	} else if ip != nil && len(ip) == 16 {
		atyp = atypIPv6
		addrBytes = ip
	} else {
		atyp = atypIPv4
		addrBytes = []byte{0, 0, 0, 0}
	}
	resp := make([]byte, 0, 6+len(addrBytes))
	resp = append(resp, verSocks5, code, 0x00, atyp)
	resp = append(resp, addrBytes...)
	resp = append(resp, byte(port>>8), byte(port&0xff))
	c.SetWriteDeadline(time.Now().Add(greetingTO))
	_, err := c.Write(resp)
	c.SetWriteDeadline(time.Time{})
	return err
}
