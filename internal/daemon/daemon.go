// Package daemon implements the background daemon that orchestrates xray,
// TUN networking, NetworkManager integration, and the Unix-socket API.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/notxaked/customv2ray/internal/config"
	"github.com/notxaked/customv2ray/internal/nm"
	"github.com/notxaked/customv2ray/internal/subscription"
	"github.com/notxaked/customv2ray/internal/tun"
	"github.com/notxaked/customv2ray/internal/xray"
)

// Daemon orchestrates every subsystem: xray core, TUN device, NM plugin,
// subscription manager, and the Unix-socket API server.
type Daemon struct {
	cfg       *config.AppConfig
	subMgr    *subscription.Manager
	xrayMgr   *xray.Manager
	tunMgr    *tun.Manager
	nmMgr     *nm.Manager
	nmPlugin  *nm.VPNPlugin
	apiServer *APIServer
	ctx       context.Context
	cancel    context.CancelFunc
	status    config.Status
	mu        sync.RWMutex
	log       *log.Logger
	startedAt time.Time
}

// New creates a new Daemon from the given application config.
func New(cfg *config.AppConfig) *Daemon {
	return &Daemon{
		cfg:     cfg,
		subMgr:  subscription.NewManager(cfg),
		xrayMgr: xray.NewManager(cfg),
		tunMgr:  tun.NewManager(cfg),
		nmMgr:   nm.NewManager(cfg),
		log:     log.New(os.Stderr, "[daemon] ", log.LstdFlags|log.Lmsgprefix),
		status: config.Status{
			Mode: cfg.Mode,
		},
	}
}

// Start initialises every sub-system and enters the main run-loop.
//
//  1. Creates runtime directories and writes the PID file.
//  2. Starts the Unix-socket API server.
//  3. Starts the subscription auto-updater.
//  4. If running as root, initialises the NetworkManager VPN plugin.
//  5. Installs signal handlers for graceful shutdown.
func (d *Daemon) Start() error {
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.startedAt = time.Now()

	// ── 1. Create runtime directories ──────────────────────────────
	dirs := []string{
		filepath.Dir(config.SocketPath), // /run/customv2ray/
		config.ConfigDir(),
		config.DataDir(),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Write PID file.
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(config.PidFile, []byte(pid), 0644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	d.log.Printf("PID %s written to %s", pid, config.PidFile)

	// ── 2. Start API server ────────────────────────────────────────
	d.apiServer = NewAPIServer(config.SocketPath, d.handleRequest)
	if err := d.apiServer.Start(d.ctx); err != nil {
		return fmt.Errorf("start api server: %w", err)
	}
	d.log.Printf("API server listening on %s", config.SocketPath)

	// ── 3. Start subscription auto-updater ─────────────────────────
	if d.cfg.AutoUpdate && d.cfg.UpdateInterval > 0 {
		go d.autoUpdateLoop()
		d.log.Printf("auto-updater started (every %d min)", d.cfg.UpdateInterval)
	}

	// ── 4. Initialise NM VPN plugin (root only) ───────────────────
	if os.Getuid() == 0 {
		if err := d.nmMgr.Init(); err != nil {
			d.log.Printf("NetworkManager init skipped: %v", err)
		} else {
			d.log.Println("NetworkManager VPN plugin initialised")
			allConfigs := d.cfg.GetAllConfigs()
			if len(allConfigs) > 0 {
				if err := d.nmMgr.SyncConfigs(allConfigs); err != nil {
					d.log.Printf("NetworkManager initial sync: %v", err)
				} else {
					d.log.Printf("NetworkManager: initially synced %d configs", len(allConfigs))
				}
			}
		}

		// Register NetworkManager VPN D-Bus service directly on system bus
		vpnPlugin, err := nm.StartDBusService(d.ctx, d.ConnectNM, d.DisconnectNM)
		if err != nil {
			d.log.Printf("NetworkManager D-Bus service error: %v", err)
		} else {
			d.log.Println("NetworkManager D-Bus VPN service active on system bus")
			d.nmPlugin = vpnPlugin
		}
	}

	// ── 5. Signal handlers ─────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case sig := <-sigCh:
			d.log.Printf("received signal %v, shutting down", sig)
			d.Stop() //nolint:errcheck
		case <-d.ctx.Done():
		}
	}()

	d.log.Println("daemon started successfully")

	// Block until context is cancelled.
	<-d.ctx.Done()
	return nil
}

// Stop tears down every component in the reverse order of Start.
func (d *Daemon) Stop() error {
	d.log.Println("stopping daemon…")

	// Disconnect active connection first.
	if err := d.Disconnect(); err != nil {
		d.log.Printf("disconnect during shutdown: %v", err)
	}

	// Stop API server.
	if d.apiServer != nil {
		if err := d.apiServer.Stop(); err != nil {
			d.log.Printf("stop api server: %v", err)
		}
	}

	// Remove PID file.
	os.Remove(config.PidFile)

	// Cancel context to unblock main goroutine.
	d.cancel()

	d.log.Println("daemon stopped")
	return nil
}

// Connect starts xray with the identified server configuration, optionally
// sets up TUN routing and activates the NM VPN connection.
func (d *Daemon) Connect(configID string) error {
	return d.connectInternal(configID, false)
}

func (d *Daemon) connectInternal(configID string, fromNM bool) error {
	d.mu.Lock()

	// 1. If already connected to this exact server, do nothing.
	if d.status.Connected && d.status.ActiveConfig != nil &&
		(d.status.ActiveConfig.ID == configID || d.status.ActiveConfig.Name == configID) {
		d.mu.Unlock()
		return nil
	}

	// 2. Look up the config.
	sc := d.cfg.FindConfig(configID)
	if sc == nil {
		d.mu.Unlock()
		return fmt.Errorf("config %q not found", configID)
	}

	// 3. Stop any running instance.
	d.disconnectLocked()

	// 4. Start xray.
	if err := d.xrayMgr.Start(d.ctx, sc); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("start xray: %w", err)
	}
	d.log.Printf("xray started for %s (%s)", sc.Name, sc.Address)

	// 5. TUN setup (if mode requires it).
	if d.cfg.Mode == config.ModeTUN || d.cfg.Mode == config.ModeBoth {
		if err := d.tunMgr.Setup(d.ctx, sc.Address); err != nil {
			d.xrayMgr.Stop() //nolint:errcheck
			d.mu.Unlock()
			return fmt.Errorf("setup tun: %w", err)
		}
		d.log.Println("TUN interface configured")
	}

	// 6. Update status.
	d.status = config.Status{
		Connected:    true,
		Mode:         d.cfg.Mode,
		ActiveConfig: sc,
		StartedAt:    time.Now(),
		ProxyAddr:    fmt.Sprintf("socks5://127.0.0.1:%d", d.cfg.SOCKSPort),
		HTTPAddr:     fmt.Sprintf("http://127.0.0.1:%d", d.cfg.HTTPPort),
	}

	d.cfg.ActiveConfigID = configID
	if err := d.cfg.Save(); err != nil {
		d.log.Printf("save config: %v", err)
	}

	// Unlock BEFORE triggering external operations.
	d.mu.Unlock()

	// Notify NetworkManager D-Bus plugin if active and not initiated by NM
	if !fromNM && d.nmPlugin != nil {
		d.nmPlugin.EmitStarted()
	}

	// 7. NM VPN activation (asynchronous, and only if not initiated from NM itself).
	if !fromNM && d.nmMgr.Available() && os.Getuid() == 0 {
		go func(serverName string) {
			if err := d.nmMgr.ActivateVPN(serverName); err != nil {
				d.log.Printf("NM VPN activation skipped: %v", err)
			} else {
				d.log.Println("NM VPN connection activated")
			}
		}(sc.Name)
	}

	return nil
}

// ConnectNM is called by the NetworkManager D-Bus service.
func (d *Daemon) ConnectNM(serverName string) error {
	return d.connectInternal(serverName, true)
}

// DisconnectNM is called by the NetworkManager D-Bus service.
func (d *Daemon) DisconnectNM() error {
	return d.disconnectInternal(true)
}

// Disconnect tears down the active connection.
func (d *Daemon) Disconnect() error {
	return d.disconnectInternal(false)
}

func (d *Daemon) disconnectInternal(fromNM bool) error {
	d.mu.Lock()
	d.disconnectLocked()
	d.mu.Unlock()

	if d.nmPlugin != nil {
		d.nmPlugin.EmitStopped()
	}

	if !fromNM && d.nmMgr.Available() && os.Getuid() == 0 {
		go func() {
			if err := d.nmMgr.DeactivateVPN(); err != nil {
				d.log.Printf("NM VPN deactivation: %v", err)
			}
		}()
	}

	return nil
}

// disconnectLocked performs the actual disconnect while holding d.mu.
func (d *Daemon) disconnectLocked() error {
	if !d.status.Connected {
		return nil
	}

	// 1. Stop xray.
	if err := d.xrayMgr.Stop(); err != nil {
		d.log.Printf("stop xray: %v", err)
	}

	// 2. Tear down TUN (if active).
	if d.cfg.Mode == config.ModeTUN || d.cfg.Mode == config.ModeBoth {
		if err := d.tunMgr.Teardown(); err != nil {
			d.log.Printf("teardown tun: %v", err)
		}
	}

	// 3. Reset status.
	d.status = config.Status{
		Mode: d.cfg.Mode,
	}
	d.cfg.ActiveConfigID = ""
	if err := d.cfg.Save(); err != nil {
		d.log.Printf("save config: %v", err)
	}

	return nil
}

// SetMode changes the operating mode (proxy / tun / both).
// If currently connected the connection is re-established with the new mode.
func (d *Daemon) SetMode(mode config.Mode) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if mode != config.ModeProxy && mode != config.ModeTUN && mode != config.ModeBoth {
		return fmt.Errorf("invalid mode %q", mode)
	}

	wasConnected := d.status.Connected
	activeID := ""
	if d.status.ActiveConfig != nil {
		activeID = d.status.ActiveConfig.ID
	}

	// Tear down current connection so networking can be reconfigured.
	d.disconnectLocked()

	d.cfg.Mode = mode
	d.status.Mode = mode
	if err := d.cfg.Save(); err != nil {
		d.log.Printf("save config: %v", err)
	}

	// Reconnect with new mode if we were connected.
	if wasConnected && activeID != "" {
		d.mu.Unlock()
		err := d.Connect(activeID)
		d.mu.Lock()
		return err
	}

	return nil
}

// GetStatus returns a snapshot of the current daemon status.
func (d *Daemon) GetStatus() config.Status {
	d.mu.RLock()
	defer d.mu.RUnlock()

	s := d.status
	if s.Connected && !s.StartedAt.IsZero() {
		s.Uptime = time.Since(s.StartedAt).Truncate(time.Second).String()
	}
	return s
}

// UpdateSubscriptions forces an immediate update of all enabled subscriptions.
func (d *Daemon) UpdateSubscriptions() error {
	if err := d.subMgr.UpdateAll(); err != nil {
		return fmt.Errorf("update subscriptions: %w", err)
	}
	if err := d.cfg.Save(); err != nil {
		return err
	}

	// Sync configs to NetworkManager so GNOME Shell VPN menu shows them immediately!
	if d.nmMgr != nil && d.nmMgr.Available() && os.Getuid() == 0 {
		allConfigs := d.cfg.GetAllConfigs()
		if err := d.nmMgr.SyncConfigs(allConfigs); err != nil {
			d.log.Printf("NM sync configs: %v", err)
		} else {
			d.log.Printf("NM: synced %d configs to NetworkManager", len(allConfigs))
		}
	}
	return nil
}

// ---------- auto-updater loop ----------

func (d *Daemon) autoUpdateLoop() {
	ticker := time.NewTicker(time.Duration(d.cfg.UpdateInterval) * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			if err := d.UpdateSubscriptions(); err != nil {
				d.log.Printf("auto-update: %v", err)
			} else {
				d.log.Println("subscriptions auto-updated")
			}
		}
	}
}

// ---------- request handler ----------

// handleRequest is the single entry-point for every API request received on
// the Unix socket. It dispatches to the appropriate method and returns a
// DaemonResponse.
func (d *Daemon) handleRequest(req config.DaemonRequest) config.DaemonResponse {
	switch req.Command {

	// ── Connection management ──────────────────────────────────────
	case "connect":
		id, ok := req.Args["config_id"]
		if !ok || id == "" {
			return errResp("config_id argument required")
		}
		if err := d.Connect(id); err != nil {
			return errResp("%s", err.Error())
		}
		return okResp("connected to %s", id)

	case "connect_nm":
		id, ok := req.Args["config_id"]
		if !ok || id == "" {
			return errResp("config_id argument required")
		}
		if err := d.connectInternal(id, true); err != nil {
			return errResp("%s", err.Error())
		}
		return okResp("connected to %s via NM", id)

	case "disconnect":
		if err := d.Disconnect(); err != nil {
			return errResp("%s", err.Error())
		}
		return okResp("disconnected")

	case "disconnect_nm":
		if err := d.disconnectInternal(true); err != nil {
			return errResp("%s", err.Error())
		}
		return okResp("disconnected via NM")

	// ── Status / health ────────────────────────────────────────────
	case "status":
		return dataResp(d.GetStatus())

	case "ping":
		return okResp("pong")

	case "nm_sync":
		if d.nmMgr != nil && d.nmMgr.Available() && os.Getuid() == 0 {
			allConfigs := d.cfg.GetAllConfigs()
			if err := d.nmMgr.SyncConfigs(allConfigs); err != nil {
				return errResp("NM sync: %v", err)
			}
			return okResp("synced %d configs to NetworkManager", len(allConfigs))
		}
		return errResp("NetworkManager not available or daemon not running as root")

	// ── Config listing ─────────────────────────────────────────────
	case "list":
		return dataResp(d.cfg.GetAllConfigs())

	case "config_show":
		raw := d.xrayMgr.CurrentConfig()
		if raw == "" {
			return errResp("no active configuration")
		}
		return dataResp(raw)

	// ── Mode ───────────────────────────────────────────────────────
	case "mode":
		m, ok := req.Args["mode"]
		if !ok || m == "" {
			return errResp("mode argument required")
		}
		if err := d.SetMode(config.Mode(m)); err != nil {
			return errResp("%s", err.Error())
		}
		return okResp("mode set to %s", m)

	// ── Subscription management ────────────────────────────────────
	case "subscribe_add":
		name := req.Args["name"]
		url := req.Args["url"]
		if name == "" || url == "" {
			return errResp("name and url arguments required")
		}
		if err := d.cfg.AddSubscription(name, url); err != nil {
			return errResp("%s", err.Error())
		}
		if err := d.cfg.Save(); err != nil {
			return errResp("%s", err.Error())
		}
		_ = d.UpdateSubscriptions()
		return okResp("subscription %q added", name)

	case "subscribe_remove":
		name := req.Args["name"]
		if name == "" {
			return errResp("name argument required")
		}
		if err := d.cfg.RemoveSubscription(name); err != nil {
			return errResp("%s", err.Error())
		}
		if err := d.cfg.Save(); err != nil {
			return errResp("%s", err.Error())
		}
		_ = d.UpdateSubscriptions()
		return okResp("subscription %q removed", name)

	case "subscribe_update":
		if err := d.UpdateSubscriptions(); err != nil {
			return errResp("%s", err.Error())
		}
		return okResp("subscriptions updated")

	case "subscribe_list":
		d.cfg.GetAllConfigs() // ensure lock ordering
		return dataResp(d.cfg.Subscriptions)

	default:
		return errResp("unknown command %q", req.Command)
	}
}

// ---------- response helpers ----------

func okResp(format string, a ...any) config.DaemonResponse {
	return config.DaemonResponse{
		Success: true,
		Message: fmt.Sprintf(format, a...),
	}
}

func errResp(format string, a ...any) config.DaemonResponse {
	return config.DaemonResponse{
		Success: false,
		Message: fmt.Sprintf(format, a...),
	}
}

func dataResp(v any) config.DaemonResponse {
	raw, err := json.Marshal(v)
	if err != nil {
		return errResp("marshal data: %v", err)
	}
	return config.DaemonResponse{
		Success: true,
		Data:    raw,
	}
}
