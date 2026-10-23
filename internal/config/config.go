// Package config defines the configuration types, defaults, and persistence
// logic for CustomV2Ray.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// AppName is the identifier for the application.
	AppName = "customv2ray"

	// Version is the current version string.
	Version = "1.0.0"

	// UserAgent is the custom User-Agent used when requesting subscriptions.
	UserAgent = "Xaked-Prilozhyha"

	// SocketPath is the default Unix domain socket path for daemon communication.
	SocketPath = "/run/customv2ray/daemon.sock"

	// PidFile is the daemon process ID file path.
	PidFile = "/run/customv2ray/daemon.pid"

	// DefaultTUN is the default TUN interface name.
	DefaultTUN = "cv2ray0"

	// DefaultSOCKS is the default local SOCKS5 inbound port (avoid 10808 to not conflict with other VPNs).
	DefaultSOCKS = 20808

	// DefaultHTTP is the default local HTTP inbound port (avoid 10809 to not conflict with other VPNs).
	DefaultHTTP = 20809

	// DefaultTProxy is the default TPROXY dokodemo-door inbound port.
	DefaultTProxy = 12345

	// DefaultDNS is the default dokodemo-door DNS inbound port.
	DefaultDNS = 5353

	// XrayBinaryName is the executable name for the Xray core.
	XrayBinaryName = "xray"
)

// Mode represents the operating proxy/TUN mode.
type Mode string

const (
	// ModeProxy routes traffic through local SOCKS5 / HTTP proxies.
	ModeProxy Mode = "proxy"

	// ModeTUN routes system-wide traffic transparently via TPROXY / TUN.
	ModeTUN Mode = "tun"

	// ModeBoth runs both system TUN and local SOCKS/HTTP proxies.
	ModeBoth Mode = "both"
)

// ConfigDir returns the configuration directory path.
// If CUSTOMV2RAY_CONFIG_DIR env var is set, it takes precedence.
// When running as root, uses /etc/customv2ray. Otherwise, ~/.config/customv2ray.
func ConfigDir() string {
	if env := os.Getenv("CUSTOMV2RAY_CONFIG_DIR"); env != "" {
		return env
	}
	if os.Getuid() == 0 {
		return "/etc/customv2ray"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/customv2ray"
	}
	return filepath.Join(home, ".config", "customv2ray")
}

// DataDir returns the data storage directory path.
// If CUSTOMV2RAY_DATA_DIR env var is set, it takes precedence.
// When running as root, uses /var/lib/customv2ray. Otherwise, ~/.local/share/customv2ray.
func DataDir() string {
	if env := os.Getenv("CUSTOMV2RAY_DATA_DIR"); env != "" {
		return env
	}
	if os.Getuid() == 0 {
		return "/var/lib/customv2ray"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/customv2ray/data"
	}
	return filepath.Join(home, ".local", "share", "customv2ray")
}

// ConfigFile returns the full path to the JSON configuration file.
func ConfigFile() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// ServerConfig represents a single server node obtained from a subscription.
type ServerConfig struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	SubName  string          `json:"sub_name"`
	Protocol string          `json:"protocol"`
	Address  string          `json:"address"`
	Port     int             `json:"port"`
	Ping     int             `json:"ping,omitempty"`
	RawJSON  json.RawMessage `json:"raw_json"`
}

// FullProtocol returns the full protocol description (e.g. "vless tcp reality", "vless xhttp reality").
func (s *ServerConfig) FullProtocol() string {
	if s == nil {
		return ""
	}
	trimmed := strings.TrimSpace(s.Protocol)
	if strings.Contains(trimmed, " ") {
		return trimmed
	}

	proto := strings.ToLower(trimmed)
	if proto == "" {
		proto = "unknown"
	}

	net := ""
	sec := ""

	if len(s.RawJSON) > 0 {
		var m map[string]interface{}
		if err := json.Unmarshal(s.RawJSON, &m); err == nil {
			var ob map[string]interface{}
			if obs, ok := m["outbounds"].([]interface{}); ok && len(obs) > 0 {
				for _, item := range obs {
					if o, ok := item.(map[string]interface{}); ok {
						p, _ := o["protocol"].(string)
						t, _ := o["tag"].(string)
						if t == "proxy" {
							ob = o
							break
						}
						if ob == nil && p != "freedom" && p != "blackhole" && p != "loopback" {
							ob = o
						}
					}
				}
				if ob == nil && len(obs) > 0 {
					ob, _ = obs[0].(map[string]interface{})
				}
			} else if _, ok := m["protocol"]; ok {
				ob = m
			}

			if ob != nil {
				if p, ok := ob["protocol"].(string); ok && p != "" {
					proto = strings.ToLower(p)
				}
				if ss, ok := ob["streamSettings"].(map[string]interface{}); ok {
					if n, ok := ss["network"].(string); ok && n != "" {
						net = strings.ToLower(n)
					}
					if s, ok := ss["security"].(string); ok && s != "" && s != "none" {
						sec = strings.ToLower(s)
					}
				}
			}
		}
	}

	parts := []string{proto}
	if net != "" {
		parts = append(parts, net)
	}
	if sec != "" {
		parts = append(parts, sec)
	}
	return strings.Join(parts, " ")
}

// Subscription represents a remote subscription source.
type Subscription struct {
	Name           string         `json:"name"`
	URL            string         `json:"url"`
	Enabled        bool           `json:"enabled"`
	AutoUpdate     bool           `json:"auto_update"`
	UpdateInterval int            `json:"update_interval"` // minutes (e.g. 60)
	UpdatedAt      time.Time      `json:"updated_at"`
	Configs        []ServerConfig `json:"configs"`
}

// Status represents the runtime status of the daemon.
type Status struct {
	Connected     bool          `json:"connected"`
	Mode          Mode          `json:"mode"`
	ActiveConfig  *ServerConfig `json:"active_config,omitempty"`
	StartedAt     time.Time     `json:"started_at,omitempty"`
	Uptime        string        `json:"uptime,omitempty"`
	ProxyAddr     string        `json:"proxy_addr,omitempty"`
	HTTPAddr      string        `json:"http_addr,omitempty"`
	UploadBytes   int64         `json:"upload_bytes,omitempty"`
	DownloadBytes int64         `json:"download_bytes,omitempty"`
	UploadRate    int64         `json:"upload_rate,omitempty"`
	DownloadRate  int64         `json:"download_rate,omitempty"`
	Error         string        `json:"error,omitempty"`
}

// DaemonRequest is sent over the Unix domain socket to the daemon.
type DaemonRequest struct {
	Command string            `json:"command"`
	Args    map[string]string `json:"args,omitempty"`
}

// DaemonResponse is returned by the daemon over the Unix domain socket.
type DaemonResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// AppConfig is the root configuration object for the application.
type AppConfig struct {
	mu             sync.RWMutex   `json:"-"`
	Mode           Mode           `json:"mode"`
	ActiveConfigID string         `json:"active_config_id,omitempty"`
	Subscriptions  []Subscription `json:"subscriptions"`
	AutoUpdate     bool           `json:"auto_update"`
	UpdateInterval int            `json:"update_interval"` // minutes (default: 60)
	SOCKSPort      int            `json:"socks_port"`
	HTTPPort       int            `json:"http_port"`
	TProxyPort     int            `json:"tproxy_port"`
	DNSPort        int            `json:"dns_port"`
	TUNName        string         `json:"tun_name"`
	LogLevel       string         `json:"log_level"`
	XrayPath       string         `json:"xray_path"`
	UserAgent      string         `json:"user_agent"`
}

// DefaultConfig returns an AppConfig initialized with recommended defaults.
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Mode:           ModeTUN,
		Subscriptions:  make([]Subscription, 0),
		AutoUpdate:     true,
		UpdateInterval: 60, // 1 hour
		SOCKSPort:      DefaultSOCKS,
		HTTPPort:       DefaultHTTP,
		TProxyPort:     DefaultTProxy,
		DNSPort:        DefaultDNS,
		TUNName:        DefaultTUN,
		LogLevel:       "warning",
		XrayPath:       XrayBinaryName,
		UserAgent:      UserAgent,
	}
}

// LoadConfig loads the configuration from disk, creating default if not exists.
func LoadConfig() (*AppConfig, error) {
	path := ConfigFile()
	data, err := os.ReadFile(path)
	if err != nil {
		// Fallback to system-wide /etc/customv2ray/config.json if user config doesn't exist
		if os.Getuid() != 0 {
			if sysData, sysErr := os.ReadFile("/etc/customv2ray/config.json"); sysErr == nil {
				data = sysData
				err = nil
			}
		}
		if err != nil {
			if os.IsNotExist(err) {
				cfg := DefaultConfig()
				_ = cfg.Save()
				return cfg, nil
			}
			return nil, fmt.Errorf("read config file: %w", err)
		}
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if cfg.UpdateInterval <= 0 {
		cfg.UpdateInterval = 60
	}
	if cfg.SOCKSPort <= 0 {
		cfg.SOCKSPort = DefaultSOCKS
	}
	if cfg.HTTPPort <= 0 {
		cfg.HTTPPort = DefaultHTTP
	}
	if cfg.TProxyPort <= 0 {
		cfg.TProxyPort = DefaultTProxy
	}
	if cfg.DNSPort <= 0 {
		cfg.DNSPort = DefaultDNS
	}
	if cfg.TUNName == "" {
		cfg.TUNName = DefaultTUN
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = UserAgent
	}

	return cfg, nil
}

// Save writes the current configuration to disk as JSON.
func (c *AppConfig) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	path := ConfigFile()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}

	return nil
}

// GetAllConfigs returns a flattened list of all server configurations across
// all enabled subscriptions.
func (c *AppConfig) GetAllConfigs() []ServerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var all []ServerConfig
	for _, sub := range c.Subscriptions {
		if !sub.Enabled {
			continue
		}
		all = append(all, sub.Configs...)
	}
	return all
}

// FindConfig searches for a server config by ID or by exact/trimmed Name.
func (c *AppConfig) FindConfig(idOrName string) *ServerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	trimmed := strings.TrimSpace(idOrName)

	// 1. Exact match by ID or trimmed Name
	for _, sub := range c.Subscriptions {
		for i := range sub.Configs {
			if sub.Configs[i].ID == trimmed || strings.TrimSpace(sub.Configs[i].Name) == trimmed {
				cp := sub.Configs[i]
				return &cp
			}
		}
	}

	// 2. Prefix match by ID
	if len(trimmed) >= 4 {
		for _, sub := range c.Subscriptions {
			for i := range sub.Configs {
				if strings.HasPrefix(sub.Configs[i].ID, trimmed) {
					cp := sub.Configs[i]
					return &cp
				}
			}
		}
	}

	// 3. Case-insensitive exact name
	lower := strings.ToLower(trimmed)
	for _, sub := range c.Subscriptions {
		for i := range sub.Configs {
			if strings.ToLower(strings.TrimSpace(sub.Configs[i].Name)) == lower {
				cp := sub.Configs[i]
				return &cp
			}
		}
	}

	return nil
}

// AddSubscription adds or updates a subscription with the given name and URL.
func (c *AppConfig) AddSubscription(name, url string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Subscriptions {
		if c.Subscriptions[i].Name == name {
			c.Subscriptions[i].URL = url
			c.Subscriptions[i].Enabled = true
			return nil
		}
	}

	c.Subscriptions = append(c.Subscriptions, Subscription{
		Name:           name,
		URL:            url,
		Enabled:        true,
		AutoUpdate:     true,
		UpdateInterval: 60,
		Configs:        make([]ServerConfig, 0),
	})
	return nil
}

// RemoveSubscription removes a subscription by name.
func (c *AppConfig) RemoveSubscription(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	idx := -1
	for i := range c.Subscriptions {
		if c.Subscriptions[i].Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("subscription %q not found", name)
	}

	c.Subscriptions = append(c.Subscriptions[:idx], c.Subscriptions[idx+1:]...)
	return nil
}

// FindSubscription finds a subscription by name.
func (c *AppConfig) FindSubscription(name string) *Subscription {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for i := range c.Subscriptions {
		if c.Subscriptions[i].Name == name {
			cp := c.Subscriptions[i]
			return &cp
		}
	}
	return nil
}
