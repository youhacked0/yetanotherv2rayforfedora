package xray

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/notxaked/customv2ray/internal/config"
)

// Manager controls the xray-core subprocess lifecycle, including starting,
// stopping, health monitoring, log capture, and config hot-reloading.
type Manager struct {
	cfg           *config.AppConfig
	cmd           *exec.Cmd
	running       bool
	mu            sync.RWMutex
	configPath    string
	logWriter     io.Writer
	onStateChange func(connected bool)
	cancel        context.CancelFunc
}

// NewManager creates a new xray process manager with the given app config.
func NewManager(cfg *config.AppConfig) *Manager {
	return &Manager{
		cfg:       cfg,
		logWriter: io.Discard,
	}
}

// SetLogWriter sets the writer that receives xray process stdout/stderr output.
func (m *Manager) SetLogWriter(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w == nil {
		w = io.Discard
	}
	m.logWriter = w
}

// SetStateCallback registers a callback invoked when the connection state
// changes (connected/disconnected).
func (m *Manager) SetStateCallback(fn func(connected bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onStateChange = fn
}

// IsRunning reports whether the xray process is currently running.
func (m *Manager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

// Start writes the xray configuration and starts the xray subprocess. It
// launches a goroutine to monitor the process and auto-restart on crash
// with exponential backoff.
func (m *Manager) Start(ctx context.Context, serverCfg *config.ServerConfig) error {
	m.mu.Lock()

	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("xray is already running")
	}

	xrayBin, err := m.getXrayPath()
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("find xray binary: %w", err)
	}

	cfgPath, err := WriteConfig(m.cfg, serverCfg)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("write xray config: %w", err)
	}
	m.configPath = cfgPath

	monitorCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	if err := m.startProcess(xrayBin); err != nil {
		m.cancel = nil
		m.mu.Unlock()
		cancel()
		return err
	}

	m.running = true
	m.mu.Unlock()

	m.notifyState(true)

	// Launch monitor goroutine for auto-restart on crash.
	go m.monitor(monitorCtx, xrayBin, serverCfg)

	return nil
}

// Stop terminates the running xray process and cancels the monitor goroutine.
func (m *Manager) Stop() error {
	m.mu.Lock()

	if !m.running {
		m.mu.Unlock()
		return nil
	}

	// Cancel monitor goroutine first to prevent auto-restart.
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	err := m.killProcess()

	m.running = false
	m.mu.Unlock()

	m.notifyState(false)

	return err
}

// CurrentConfig returns the active xray config JSON content, or empty if none.
func (m *Manager) CurrentConfig() string {
	m.mu.RLock()
	path := m.configPath
	m.mu.RUnlock()

	if path == "" {
		path = CurrentConfigPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// Restart stops the current xray process and starts a new one with the
// given server config.
func (m *Manager) Restart(serverCfg *config.ServerConfig) error {
	if err := m.Stop(); err != nil {
		return fmt.Errorf("stop before restart: %w", err)
	}
	return m.Start(context.Background(), serverCfg)
}

// GetXrayPath finds the xray binary path. It checks (in order):
//  1. AppConfig.XrayPath (user-configured explicit path)
//  2. System PATH
//  3. ~/.local/share/customv2ray/xray
func (m *Manager) GetXrayPath() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getXrayPath()
}

// getXrayPath is the non-locking implementation of GetXrayPath.
func (m *Manager) getXrayPath() (string, error) {
	// 1. Check user-configured path.
	if m.cfg.XrayPath != "" {
		if _, err := os.Stat(m.cfg.XrayPath); err == nil {
			return m.cfg.XrayPath, nil
		}
	}

	// 2. Check system PATH.
	if p, err := exec.LookPath(config.XrayBinaryName); err == nil {
		return p, nil
	}

	// 3. Check data directory (~/.local/share/customv2ray/xray).
	localPath := filepath.Join(config.DataDir(), config.XrayBinaryName)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	return "", fmt.Errorf(
		"xray binary not found: checked XrayPath=%q, $PATH, and %s",
		m.cfg.XrayPath, localPath,
	)
}

// startProcess spawns the xray subprocess and sets up log piping.
// Caller must hold m.mu.
func (m *Manager) startProcess(xrayBin string) error {
	m.cmd = exec.Command(xrayBin, "run", "-config", m.configPath)
	m.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	stdout, err := m.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create stdout pipe: %w", err)
	}
	stderr, err := m.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("start xray process: %w", err)
	}

	log.Printf("[daemon] xray core started (PID %d, config=%s)", m.cmd.Process.Pid, m.configPath)

	// Forward stdout and stderr to the configured log writer.
	go m.pipeLogs("stdout", stdout)
	go m.pipeLogs("stderr", stderr)

	return nil
}

// killProcess sends SIGTERM and waits for the process to exit.
// Caller must hold m.mu.
func (m *Manager) killProcess() error {
	if m.cmd == nil || m.cmd.Process == nil {
		return nil
	}

	// Send SIGTERM for graceful shutdown.
	if err := m.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Process may already be dead; try to reap.
		_ = m.cmd.Wait()
		return nil
	}

	// Wait with timeout for graceful exit.
	done := make(chan error, 1)
	go func() {
		done <- m.cmd.Wait()
	}()

	select {
	case <-done:
		// Exited cleanly.
	case <-time.After(5 * time.Second):
		// Force kill if still alive.
		_ = m.cmd.Process.Kill()
		<-done
	}

	m.cmd = nil
	return nil
}

// monitor watches the xray process and restarts it on unexpected exit
// with exponential backoff (1s, 2s, 4s, …, max 60s).
func (m *Manager) monitor(ctx context.Context, xrayBin string, serverCfg *config.ServerConfig) {
	const (
		initialBackoff = 1 * time.Second
		maxBackoff     = 60 * time.Second
	)
	backoff := initialBackoff

	for {
		// Wait for the process to exit.
		m.mu.RLock()
		cmd := m.cmd
		m.mu.RUnlock()

		if cmd == nil {
			return
		}

		// Wait for exit in a goroutine so we can also react to context cancellation.
		exitCh := make(chan error, 1)
		go func() {
			exitCh <- cmd.Wait()
		}()

		select {
		case <-ctx.Done():
			// Ordered shutdown; don't restart.
			return
		case err := <-exitCh:
			// Check if shutdown was requested while we were waiting.
			select {
			case <-ctx.Done():
				return
			default:
			}

			m.mu.Lock()
			m.running = false
			m.cmd = nil
			m.mu.Unlock()
			m.notifyState(false)

			if err != nil {
				m.writeLog("xray process exited unexpectedly: %v", err)
			} else {
				m.writeLog("xray process exited with status 0")
			}

			m.writeLog("restarting in %v...", backoff)

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			// Exponential backoff.
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}

			// Rebuild config in case settings changed.
			cfgPath, err := WriteConfig(m.cfg, serverCfg)
			if err != nil {
				m.writeLog("failed to rebuild config for restart: %v", err)
				continue
			}

			m.mu.Lock()
			m.configPath = cfgPath
			if startErr := m.startProcess(xrayBin); startErr != nil {
				m.mu.Unlock()
				m.writeLog("failed to restart xray: %v", startErr)
				continue
			}
			m.running = true
			m.mu.Unlock()
			m.notifyState(true)

			// Reset backoff on successful restart.
			backoff = initialBackoff
		}
	}
}

// ReloadConfig sends SIGUSR1 to the running xray process to trigger a
// configuration hot-reload. The config file at configPath must be updated
// before calling this method.
func (m *Manager) ReloadConfig() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.running || m.cmd == nil || m.cmd.Process == nil {
		return fmt.Errorf("xray is not running")
	}

	if err := m.cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		return fmt.Errorf("send SIGUSR1 to xray: %w", err)
	}
	return nil
}

// pipeLogs reads lines from the given reader and writes them to the log writer.
func (m *Manager) pipeLogs(name string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		m.mu.RLock()
		w := m.logWriter
		m.mu.RUnlock()

		fmt.Fprintf(w, "[xray/%s] %s\n", name, line)
		log.Printf("[xray/%s] %s", name, line)
	}
}

// writeLog writes a formatted log message to the configured log writer.
func (m *Manager) writeLog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	m.mu.RLock()
	w := m.logWriter
	m.mu.RUnlock()
	fmt.Fprintf(w, "[xray/manager] %s\n", msg)
	log.Printf("[xray/manager] %s", msg)
}

// notifyState calls the state change callback if one is registered.
// Caller must NOT hold m.mu as the callback may call back into the Manager.
func (m *Manager) notifyState(connected bool) {
	m.mu.RLock()
	fn := m.onStateChange
	m.mu.RUnlock()
	if fn != nil {
		fn(connected)
	}
}
