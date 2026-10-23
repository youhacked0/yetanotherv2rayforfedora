// Package nm manages NetworkManager integration for GNOME panel VPN display.
//
// It installs a custom NM VPN plugin descriptor, creates/removes VPN
// connections via nmcli, and activates/deactivates them so that GNOME Shell
// shows the VPN icon in the system tray.
package nm

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/notxaked/customv2ray/internal/config"
)

const (
	// vpnServiceName is the D-Bus service name for our NM VPN plugin.
	vpnServiceName = "org.freedesktop.NetworkManager.customv2ray"

	// nmVPNDir is where NM looks for VPN plugin descriptors.
	nmVPNDir = "/etc/NetworkManager/VPN"

	// nmVPNPluginFile is the filename of our plugin descriptor.
	nmVPNPluginFile = "customv2ray.name"

	// dbusServiceDir is where D-Bus system service activation files live.
	dbusServiceDir = "/usr/share/dbus-1/system-services"

	// dbusServiceFile is our D-Bus service activation file name.
	dbusServiceFile = "org.freedesktop.NetworkManager.customv2ray.service"

	// defaultConnPrefix is the prefix for NM VPN connection names.
	defaultConnPrefix = "CustomV2Ray"
)

// Manager manages NetworkManager VPN connections.
type Manager struct {
	cfg      *config.AppConfig
	connUUID string
	connName string
	active   bool
	mu       sync.Mutex
}

// NewManager creates a new NM manager.
func NewManager(cfg *config.AppConfig) *Manager {
	return &Manager{
		cfg:      cfg,
		connName: defaultConnPrefix,
	}
}

// EnsureVPNPlugin installs the NM VPN plugin descriptor and D-Bus service
// activation file if they are not already present. Requires root.
func (m *Manager) EnsureVPNPlugin() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	binPath := "/usr/local/bin/" + config.AppName
	if _, err := os.Stat(binPath); err != nil {
		if self, err := os.Executable(); err == nil {
			self, _ = filepath.EvalSymlinks(self)
			binPath = self
		}
	}

	// --- NM VPN plugin descriptor ---
	pluginPath := filepath.Join(nmVPNDir, nmVPNPluginFile)
	pluginContent := fmt.Sprintf(`[VPN Connection]
name=customv2ray
service=%s
program=%s
supports-multiple-connections=false
`, vpnServiceName, binPath)

	if err := writeFileIfChanged(pluginPath, pluginContent, 0644); err != nil {
		return fmt.Errorf("nm: install VPN plugin file: %w", err)
	}

	// --- D-Bus system service activation file ---
	dbusPath := filepath.Join(dbusServiceDir, dbusServiceFile)
	dbusContent := fmt.Sprintf(`[D-BUS Service]
Name=%s
Exec=%s --bus-name %s
User=root
SystemdService=customv2ray.service
`, vpnServiceName, binPath, vpnServiceName)

	if err := writeFileIfChanged(dbusPath, dbusContent, 0644); err != nil {
		return fmt.Errorf("nm: install D-Bus service file: %w", err)
	}

	// --- D-Bus policy file (allow our service on the system bus) ---
	policyPath := "/etc/dbus-1/system.d/customv2ray.conf"
	policyContent := fmt.Sprintf(`<!DOCTYPE busconfig PUBLIC
 "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <policy user="root">
    <allow own="%s"/>
    <allow send_destination="%s"/>
  </policy>
  <policy context="default">
    <allow send_destination="%s"/>
  </policy>
</busconfig>
`, vpnServiceName, vpnServiceName, vpnServiceName)

	if err := writeFileIfChanged(policyPath, policyContent, 0644); err != nil {
		return fmt.Errorf("nm: install D-Bus policy file: %w", err)
	}

	log.Printf("nm: VPN plugin files installed")
	return nil
}

// CreateConnection creates a NM VPN connection with the given name.
// If a connection with the same name already exists, it is reused.
func (m *Manager) CreateConnection(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	connName := strings.TrimSpace(name)

	// Check if already exists.
	uuid, err := nmcliGetUUID(connName)
	if err == nil && uuid != "" {
		m.connUUID = uuid
		m.connName = connName
		return nil
	}

	// Create new VPN connection.
	out, err := nmcliRun("connection", "add",
		"type", "vpn",
		"con-name", connName,
		"vpn-type", "customv2ray",
		"autoconnect", "no",
	)
	if err != nil {
		return fmt.Errorf("nm: create connection %q: %w (%s)", connName, err, out)
	}

	// Retrieve the UUID of the newly created connection.
	uuid, err = nmcliGetUUID(connName)
	if err != nil {
		return fmt.Errorf("nm: get uuid for %q: %w", connName, err)
	}

	m.connUUID = uuid
	m.connName = connName

	log.Printf("nm: created VPN connection %q (uuid=%s)", connName, uuid)
	return nil
}

// Available reports whether NetworkManager tools (nmcli) are present on the system.
func (m *Manager) Available() bool {
	_, err := exec.LookPath("nmcli")
	return err == nil
}

// Init ensures that the NetworkManager VPN plugin descriptor and D-Bus policies
// are installed.
func (m *Manager) Init() error {
	return m.EnsureVPNPlugin()
}

// ActivateVPN activates the VPN connection for a given server name, or the default.
func (m *Manager) ActivateVPN(name ...string) error {
	if len(name) > 0 && name[0] != "" {
		clean := strings.TrimSpace(name[0])
		if err := m.CreateConnection(clean); err != nil {
			return err
		}
	}
	return m.ActivateConnection()
}

// DeactivateVPN deactivates the currently active VPN connection.
func (m *Manager) DeactivateVPN() error {
	return m.DeactivateConnection()
}

// ActivateConnection activates the NM VPN connection, causing the GNOME
// panel to show the VPN icon.
func (m *Manager) ActivateConnection() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.connName == "" {
		return fmt.Errorf("nm: no connection configured")
	}

	out, err := nmcliRun("connection", "up", m.connName)
	if err != nil {
		return fmt.Errorf("nm: activate %q: %w (%s)", m.connName, err, out)
	}

	m.active = true
	log.Printf("nm: activated connection %q", m.connName)
	return nil
}

// DeactivateConnection deactivates the NM VPN connection.
func (m *Manager) DeactivateConnection() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.connName == "" {
		return nil
	}

	out, err := nmcliRun("connection", "down", m.connName)
	if err != nil {
		// Not fatal; connection may already be down.
		log.Printf("nm: deactivate %q: %v (%s)", m.connName, err, out)
	}

	m.active = false
	return nil
}

// RemoveConnection removes the NM VPN connection entirely.
func (m *Manager) RemoveConnection() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.connName == "" {
		return nil
	}

	// Deactivate first.
	_, _ = nmcliRun("connection", "down", m.connName)

	out, err := nmcliRun("connection", "delete", m.connName)
	if err != nil {
		return fmt.Errorf("nm: remove %q: %w (%s)", m.connName, err, out)
	}

	log.Printf("nm: removed connection %q", m.connName)
	m.connUUID = ""
	m.connName = defaultConnPrefix
	m.active = false
	return nil
}

// NMConn represents a NetworkManager connection name and UUID.
type NMConn struct {
	Name string
	UUID string
}

// SyncConfigs ensures that NM connections reflect the supplied server configs.
// It removes duplicates (by UUID), removes stale connections (including old "CustomV2Ray - *" connections),
// and creates missing ones without any prefix.
func (m *Manager) SyncConfigs(configs []config.ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, err := listCV2RayConnections()
	if err != nil {
		return fmt.Errorf("nm: list connections: %w", err)
	}

	// Build desired set: map from normalized connection name to bool
	desired := make(map[string]bool, len(configs))
	for _, c := range configs {
		cleanName := strings.TrimSpace(c.Name)
		desired[cleanName] = true
	}

	// Track seen names to remove duplicates
	seen := make(map[string]bool)

	// Remove connections that are not desired or are duplicates (always delete by UUID!)
	for _, conn := range existing {
		cleanName := strings.TrimSpace(conn.Name)
		if !desired[cleanName] || seen[cleanName] {
			out, err := nmcliRun("connection", "delete", "uuid", conn.UUID)
			if err != nil {
				log.Printf("nm: sync: failed to remove %s (%s): %v (%s)", conn.Name, conn.UUID, err, out)
			} else {
				log.Printf("nm: sync: removed duplicate/stale %s (%s)", conn.Name, conn.UUID)
			}
		} else {
			seen[cleanName] = true
		}
	}

	// Create missing connections
	for _, c := range configs {
		cleanName := strings.TrimSpace(c.Name)
		if seen[cleanName] {
			continue
		}

		out, err := nmcliRun("connection", "add",
			"type", "vpn",
			"con-name", cleanName,
			"vpn-type", "customv2ray",
			"autoconnect", "no",
		)
		if err != nil {
			log.Printf("nm: sync: failed to create %q: %v (%s)", cleanName, err, out)
		} else {
			seen[cleanName] = true
			log.Printf("nm: sync: created connection %q", cleanName)
		}
	}

	return nil
}

// listCV2RayConnections returns names and UUIDs of all NM connections that belong to CustomV2Ray.
func listCV2RayConnections() ([]NMConn, error) {
	out, err := nmcliRun("--terse", "--fields", "NAME,UUID,TYPE", "connection", "show")
	if err != nil {
		return nil, err
	}

	var result []NMConn
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: NAME:UUID:TYPE
		// Since NAME can contain colons, we parse UUID and TYPE from the right
		lastIdx := strings.LastIndex(line, ":")
		if lastIdx == -1 {
			continue
		}
		connType := strings.TrimSpace(line[lastIdx+1:])
		remainder := line[:lastIdx]

		secondLastIdx := strings.LastIndex(remainder, ":")
		if secondLastIdx == -1 {
			continue
		}
		uuid := strings.TrimSpace(remainder[secondLastIdx+1:])
		name := strings.TrimSpace(remainder[:secondLastIdx])
		name = strings.ReplaceAll(name, `\:`, `:`)

		// 1. Match legacy prefix "CustomV2Ray" (to clean up old ones)
		if strings.HasPrefix(name, defaultConnPrefix) {
			result = append(result, NMConn{Name: name, UUID: uuid})
			continue
		}

		// 2. Match VPN connections belonging to our customv2ray plugin
		if connType == "vpn" {
			stOut, err := nmcliRun("-g", "vpn.service-type", "connection", "show", "uuid", uuid)
			if err == nil && (strings.Contains(stOut, "customv2ray") || strings.Contains(stOut, vpnServiceName)) {
				result = append(result, NMConn{Name: name, UUID: uuid})
			}
		}
	}
	return result, nil
}

// IsActive returns whether a NM VPN connection is currently active.
func (m *Manager) IsActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// ---------- helpers ----------

// nmcliRun executes nmcli with the given arguments and returns combined output.
func nmcliRun(args ...string) (string, error) {
	cmd := exec.Command("nmcli", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// nmcliGetUUID returns the UUID of a NM connection by name.
func nmcliGetUUID(name string) (string, error) {
	out, err := nmcliRun("--terse", "--fields", "connection.uuid",
		"connection", "show", name)
	if err != nil {
		return "", err
	}

	// Output format: "connection.uuid:UUID-HERE"
	parts := strings.SplitN(out, ":", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[1]), nil
	}
	return strings.TrimSpace(out), nil
}



// writeFileIfChanged writes content to path only if the content differs from
// what is already on disk. It creates parent directories as needed.
func writeFileIfChanged(path, content string, perm os.FileMode) error {
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == content {
		return nil // Already up to date.
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	return os.WriteFile(path, []byte(content), perm)
}
