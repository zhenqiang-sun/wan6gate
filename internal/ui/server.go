package ui

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"wan6gate/internal/banlist"
	"wan6gate/internal/config"
	"wan6gate/internal/ddns"
	"wan6gate/internal/forwarder"
	"wan6gate/internal/historystore"
	"wan6gate/internal/notify"
	"wan6gate/internal/socks5"
	"wan6gate/internal/tasks"
)

//go:embed web
var webFS embed.FS

type Server struct {
	cfg      *config.Config
	cfgPath  string
	sessions *config.SessionStore
	fwdMgr   *forwarder.Manager
	socksMgr *socks5.Manager
	ddnsMgr  *ddns.Manager
	notif    *notify.Notifier
	bans     *banlist.BanList
	history  *historystore.Store
	taskMgr  *tasks.Manager
	log      *slog.Logger
	hub      *LogHub

	startedAt time.Time
	runCtx    context.Context
	mu        sync.Mutex
}

func NewServer(cfg *config.Config, cfgPath string, sess *config.SessionStore, fwdMgr *forwarder.Manager, socksMgr *socks5.Manager, ddnsMgr *ddns.Manager, notif *notify.Notifier, bans *banlist.BanList, history *historystore.Store, taskMgr *tasks.Manager, log *slog.Logger, hub *LogHub) *Server {
	return &Server{
		cfg:       cfg,
		cfgPath:   cfgPath,
		sessions:  sess,
		fwdMgr:    fwdMgr,
		socksMgr:  socksMgr,
		ddnsMgr:   ddnsMgr,
		notif:     notif,
		bans:      bans,
		history:   history,
		taskMgr:   taskMgr,
		log:       log,
		hub:       hub,
		startedAt: time.Now(),
	}
}

// SetRunCtx stores the long-lived context used for spawning forwarders.
func (s *Server) SetRunCtx(ctx context.Context) {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
}

// RunCtx returns the ctx (or context.Background fallback).
func (s *Server) RunCtx() context.Context {
	s.mu.Lock()
	c := s.runCtx
	s.mu.Unlock()
	if c == nil {
		return context.Background()
	}
	return c
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	s.routes(mux)

	srv := &http.Server{
		Addr:              s.cfg.UI.BindAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.log.Info("UI server starting", "addr", s.cfg.UI.BindAddr)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) routes(mux *http.ServeMux) {
	// Static assets from embed.FS subtree "web/"
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Should not happen at runtime if files exist at build time
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", fileServer)

	// Setup (no auth required)
	mux.HandleFunc("/api/setup/status", s.handleSetupStatus)
	mux.HandleFunc("/api/setup/init", s.handleSetupInit)

	// Auth (login/logout don't require existing session; change-password does)
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/api/auth/change-password", s.requireAuth(s.handleChangePassword))

	// Authenticated APIs
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/overview", s.requireAuth(s.handleOverview))
	mux.HandleFunc("/api/mappings", s.requireAuth(s.handleMappings))
	mux.HandleFunc("/api/mappings/", s.requireAuth(s.handleMappingItem))
	mux.HandleFunc("/api/logs", s.requireAuth(s.handleLogsSSE))

	// DDNS
	mux.HandleFunc("/api/ddns", s.requireAuth(s.handleDDNS))
	mux.HandleFunc("/api/ddns/sync", s.requireAuth(s.handleDDNSSync))
	mux.HandleFunc("/api/ddns/test", s.requireAuth(s.handleDDNSTest))

	// Notify
	mux.HandleFunc("/api/notify", s.requireAuth(s.handleNotify))
	mux.HandleFunc("/api/notify/test", s.requireAuth(s.handleNotifyTest))

	// SOCKS5 (independent from /api/mappings)
	mux.HandleFunc("/api/socks5", s.requireAuth(s.handleSocks5List))
	mux.HandleFunc("/api/socks5/", s.requireAuth(s.handleSocks5Item))

	// Metrics
	mux.HandleFunc("/api/metrics", s.requireAuth(s.handleMetrics))

	// Ban list (P9)
	mux.HandleFunc("/api/banlist", s.requireAuth(s.handleBanList))
	mux.HandleFunc("/api/banlist/", s.requireAuth(s.handleBanListItem))

	// Client config (P7)
	mux.HandleFunc("/api/socks5-clientconfig/", s.requireAuth(s.handleSocks5ClientConfig))

	// History (P8)
	mux.HandleFunc("/api/history", s.requireAuth(s.handleHistory))

	// Tasks
	mux.HandleFunc("/api/tasks", s.requireAuth(s.handleTasks))
	mux.HandleFunc("/api/tasks/", s.requireAuth(s.handleTaskItem))
}
