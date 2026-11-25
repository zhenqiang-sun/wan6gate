package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"wan6gate/internal/banlist"
	"wan6gate/internal/config"
	"wan6gate/internal/ddns"
	"wan6gate/internal/forwarder"
	"wan6gate/internal/historystore"
	"wan6gate/internal/notify"
	"wan6gate/internal/persistence"
	"wan6gate/internal/rotator"
	"wan6gate/internal/socks5"
	"wan6gate/internal/tasks"
	"wan6gate/internal/ui"
	"wan6gate/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]
	switch sub {
	case "daemon":
		runDaemon(args)
	case "install":
		runInstall(args)
	case "uninstall":
		runUninstall(args)
	case "version", "-v", "--version":
		fmt.Println(version.Full())
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`Usage: wan6gate <command> [options]

Commands:
  daemon       Run forwarders + Web UI in foreground
  install      Install boot hooks (cron watchdog) and prepare /data dirs
  uninstall    Remove boot hooks; drops uci firewall rules
  version      Show version
  help         Show this help

Daemon options:
  --config <path>      Path to config.json (default $WAN6GATE_DIR/config.json or /data/router-ops/wan6gate/config.json)

Uninstall options:
  --keep-config        Keep config.json and binary (default true)
  --purge              Delete config.json and binary`)
}

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	cfgPath := fs.String("config", config.ConfigPath(), "Path to config.json")
	bindOverride := fs.String("bind", "", "Override UI bind addr (host:port); empty = use config")
	fs.Parse(args)

	if err := os.MkdirAll(filepath.Dir(*cfgPath), 0700); err != nil {
		fail("mkdir config dir: %v", err)
	}
	if err := os.MkdirAll(config.LogDir(), 0700); err != nil {
		fail("mkdir log dir: %v", err)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fail("load config: %v", err)
	}
	if *bindOverride != "" {
		cfg.UI.BindAddr = *bindOverride
	}
	// Persist defaults if first run
	if err := cfg.Save(*cfgPath); err != nil {
		fail("save config: %v", err)
	}

	// Singleton lock
	lockFD, err := acquireLock(config.LockPath())
	if err != nil {
		fail("another wan6gate daemon appears to be running: %v", err)
	}
	defer syscall.Close(lockFD)

	// PID file
	if err := writePIDFile(config.PIDPath()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write pidfile: %v\n", err)
	}
	defer os.Remove(config.PIDPath())

	// Logging: stderr + rotating file + LogHub for SSE
	logFile, err := openLogFile(config.LogPath())
	if err != nil {
		fail("open log: %v", err)
	}
	defer logFile.Close()

	hub := ui.NewLogHub(300)
	// Log goes to file only; cron's >>"$LOG" redirect captures any stray
	// stderr output (e.g. Go runtime panics). Avoid passing os.Stderr here
	// or every line ends up duplicated when launched by the watchdog.
	log := ui.MakeLogger(hub, logFile)

	log.Info("wan6gate starting", "version", version.Full(), "config", *cfgPath, "pid", os.Getpid())

	// Wire managers + notify + UI
	sessions := config.NewSessionStore()
	fwdMgr := forwarder.NewManager(log)
	bans := banlist.New()
	socksMgr := socks5.NewManager(log, bans)
	notifier := notify.New(cfg, log)
	bans.SetOnBan(notifier.OnSocks5Ban)
	ddnsMgr := ddns.NewManager(cfg, *cfgPath, log, notifier)
	history := historystore.New(filepath.Join(config.BaseDir(), "metrics"), log)
	taskMgr := tasks.NewManager(cfg, *cfgPath, log, notifier)
	srv := ui.NewServer(cfg, *cfgPath, sessions, fwdMgr, socksMgr, ddnsMgr, notifier, bans, history, taskMgr, log, hub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigs
		log.Info("signal received, shutting down", "sig", s.String())
		cancel()
	}()

	srv.SetRunCtx(ctx)
	fwdMgr.Reconcile(ctx, cfg.ListMappings())
	socksMgr.Reconcile(ctx, cfg.ListSocks5())

	// Fire daemon-started notification (no-op if disabled / unconfigured).
	notifier.Send(notify.Event{
		Type:   notify.EventDaemonStarted,
		Title:  "wan6gate started",
		Detail: fmt.Sprintf("version %s\npid=%d\nbind=%s", version.Full(), os.Getpid(), cfg.UI.BindAddr),
	})

	// DDNS goroutine: only does anything if ddns.enabled
	go ddnsMgr.Run(ctx)

	// History store goroutine: hourly metric snapshots + retention GC.
	go history.Run()

	// Task scheduler loop
	taskMgr.Start(ctx)

	if err := srv.Run(ctx); err != nil {
		log.Error("UI server error", "err", err)
	}
	fwdMgr.StopAll()
	socksMgr.StopAll()
	log.Info("shutdown complete")
}

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	fs.Parse(args)
	if err := persistence.Install(); err != nil {
		fail("install: %v", err)
	}
	fmt.Println("\nInstalled.")
	fmt.Println("Open  http://<router-lan-ip>:18080  to set up the admin password.")
	fmt.Println("Watchdog runs every minute via cron and re-launches the daemon if needed.")
}

func runUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	keep := fs.Bool("keep-config", true, "keep config.json and binary")
	purge := fs.Bool("purge", false, "remove config.json and binary too")
	fs.Parse(args)
	if err := persistence.Uninstall(*keep && !*purge); err != nil {
		fail("uninstall: %v", err)
	}
	fmt.Println("Uninstalled.")
	if *purge {
		fmt.Println("Config and binary removed.")
	} else {
		fmt.Println("Config kept at", config.ConfigPath())
	}
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "wan6gate: "+format+"\n", args...)
	os.Exit(1)
}

func acquireLock(path string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return -1, err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR, 0600)
	if err != nil {
		return -1, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		return -1, errors.New("locked by another process")
	}
	return fd, nil
}

func writePIDFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// openLogFile opens log with active runtime rotation (max 2MB per file, 2 backups).
func openLogFile(path string) (io.WriteCloser, error) {
	return rotator.New(path, rotator.Options{
		MaxSize:    2 * 1024 * 1024,
		MaxBackups: 2,
	})
}
