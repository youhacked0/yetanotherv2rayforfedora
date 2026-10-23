// Package tun manages TUN-based transparent proxying via iptables TPROXY.
//
// It sets up iptables mangle rules that redirect all TCP/UDP traffic through
// xray's dokodemo-door inbound (TPROXY mode), while excluding private networks
// and the real VPN server IP to prevent routing loops.
package tun

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/notxaked/customv2ray/internal/config"
)

const (
	// DefaultTProxyPort is the default port for xray's dokodemo-door TPROXY inbound.
	DefaultTProxyPort = 12345

	// chainName is the iptables chain for TPROXY (PREROUTING).
	chainName = "CV2RAY"

	// markChainName is the iptables chain for local OUTPUT marking.
	markChainName = "CV2RAY_MARK"

	defaultFWMark  = 1
	defaultTableID = 100
)

// privateNetworks are excluded from TPROXY redirection.
var privateNetworks = []string{
	"127.0.0.0/8",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"224.0.0.0/4",
	"240.0.0.0/4",
}

// Manager handles TUN/TPROXY setup and teardown.
type Manager struct {
	cfg        *config.AppConfig
	active     bool
	mu         sync.Mutex
	tunName    string
	origDNS    []string
	tableID    int
	markID     int
	tproxyPort int
	serverIP   string
	xrayUID    string
}

// NewManager creates a new TUN manager.
func NewManager(cfg *config.AppConfig) *Manager {
	tunName := cfg.TUNName
	if tunName == "" {
		tunName = config.DefaultTUN
	}
	return &Manager{
		cfg:        cfg,
		tunName:    tunName,
		tableID:    defaultTableID,
		markID:     defaultFWMark,
		tproxyPort: DefaultTProxyPort,
	}
}

// Setup configures TPROXY-based transparent proxying.
// serverIP is the real address of the VPN server that must be excluded from
// redirection to prevent routing loops. Requires root.
func (m *Manager) Setup(ctx context.Context, serverIP string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active {
		return fmt.Errorf("tun: already active")
	}

	// Resolve server IP if it's a hostname (non-fatal if DNS is temporarily unavailable).
	resolved, err := resolveIP(serverIP)
	if err != nil {
		log.Printf("tun: warning: could not resolve server IP %q: %v (continuing)", serverIP, err)
	} else {
		m.serverIP = resolved
		log.Printf("tun: resolved server %s -> %s", serverIP, resolved)
	}

	// Detect the UID that xray runs as (current user, since daemon runs xray).
	m.xrayUID = fmt.Sprintf("%d", os.Getuid())

	// Create tun/dummy network device for NetworkManager and systemd-resolved.
	_ = runQuiet("ip", "link", "del", m.tunName)
	if err := runQuiet("ip", "tuntap", "add", "dev", m.tunName, "mode", "tun"); err != nil {
		_ = runQuiet("ip", "link", "add", m.tunName, "type", "dummy")
	}
	_ = runQuiet("ip", "link", "set", m.tunName, "up")
	_ = runQuiet("ip", "addr", "add", "198.18.0.1/32", "dev", m.tunName)

	// Save original DNS before we change it.
	m.origDNS, err = currentDNSServers()
	if err != nil {
		log.Printf("tun: warning: could not read current DNS: %v", err)
	}

	// Step 1: Enable IP forwarding.
	if err := sysctl("net.ipv4.ip_forward", "1"); err != nil {
		return fmt.Errorf("tun: enable ip_forward: %w", err)
	}

	// Step 2: Set up policy routing (route marked packets through local loopback).
	if err := m.setupRouting(); err != nil {
		_ = m.cleanupRouting()
		return fmt.Errorf("tun: setup routing: %w", err)
	}

	// Step 3: Set up iptables TPROXY rules.
	if err := m.setupIPTables(); err != nil {
		_ = m.cleanupIPTables()
		_ = m.cleanupRouting()
		return fmt.Errorf("tun: setup iptables: %w", err)
	}

	// Step 4: Configure system DNS to use xray's DNS.
	dnsAddr := fmt.Sprintf("127.0.0.1:%d", m.cfg.DNSPort)
	if err := m.setSystemDNS(dnsAddr); err != nil {
		log.Printf("tun: warning: failed to set system DNS: %v", err)
		// Non-fatal; continue.
	}

	m.active = true
	log.Printf("tun: TPROXY setup complete (server=%s, tproxy-port=%d, mark=0x%x, table=%d)",
		m.serverIP, m.tproxyPort, m.markID, m.tableID)
	return nil
}

// Teardown removes all TPROXY rules, routing, and restores DNS.
func (m *Manager) Teardown() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.active {
		return nil
	}

	var errs []string

	// Remove iptables rules.
	if err := m.cleanupIPTables(); err != nil {
		errs = append(errs, fmt.Sprintf("iptables cleanup: %v", err))
	}

	// Remove routing rules.
	if err := m.cleanupRouting(); err != nil {
		errs = append(errs, fmt.Sprintf("routing cleanup: %v", err))
	}

	// Restore DNS.
	if err := m.restoreSystemDNS(); err != nil {
		errs = append(errs, fmt.Sprintf("dns restore: %v", err))
	}

	// Remove network interface.
	_ = runQuiet("ip", "link", "del", m.tunName)

	m.active = false
	m.serverIP = ""

	if len(errs) > 0 {
		return fmt.Errorf("tun: teardown errors: %s", strings.Join(errs, "; "))
	}

	log.Printf("tun: teardown complete")
	return nil
}

// IsActive returns whether TPROXY is currently active.
func (m *Manager) IsActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// TProxyPort returns the TPROXY port for use in xray config generation.
func (m *Manager) TProxyPort() int {
	return m.tproxyPort
}

// BuildTProxyInbound returns a dokodemo-door inbound configuration map
// suitable for xray's JSON config when TUN/TPROXY mode is active.
func BuildTProxyInbound(port int) map[string]interface{} {
	return map[string]interface{}{
		"tag":      "tproxy-in",
		"port":     port,
		"protocol": "dokodemo-door",
		"settings": map[string]interface{}{
			"network":      "tcp,udp",
			"followRedirect": true,
		},
		"sniffing": map[string]interface{}{
			"enabled":      true,
			"destOverride": []string{"http", "tls", "quic"},
		},
		"streamSettings": map[string]interface{}{
			"sockopt": map[string]interface{}{
				"tproxy": "tproxy",
			},
		},
	}
}

// ---------- routing helpers ----------

func (m *Manager) setupRouting() error {
	// Clean up any stale rule/route first
	_ = runQuiet("ip", "rule", "del", "fwmark", fmt.Sprintf("%d", m.markID), "table", fmt.Sprintf("%d", m.tableID))

	// ip rule add fwmark <mark> table <table> pref 100
	if err := run("ip", "rule", "add", "fwmark",
		fmt.Sprintf("%d", m.markID), "table", fmt.Sprintf("%d", m.tableID), "pref", "100"); err != nil {
		return fmt.Errorf("add ip rule: %w", err)
	}

	// ip route replace local 0.0.0.0/0 dev lo table <table>
	if err := run("ip", "route", "replace", "local", "0.0.0.0/0",
		"dev", "lo", "table", fmt.Sprintf("%d", m.tableID)); err != nil {
		return fmt.Errorf("add ip route: %w", err)
	}

	return nil
}

func (m *Manager) cleanupRouting() error {
	var firstErr error
	collect := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	collect(runQuiet("ip", "route", "del", "local", "0.0.0.0/0",
		"dev", "lo", "table", fmt.Sprintf("%d", m.tableID)))
	collect(runQuiet("ip", "rule", "del", "fwmark",
		fmt.Sprintf("%d", m.markID), "table", fmt.Sprintf("%d", m.tableID)))

	return firstErr
}

// ---------- iptables helpers ----------

func (m *Manager) setupIPTables() error {
	mark := fmt.Sprintf("%d", m.markID)
	port := fmt.Sprintf("%d", m.tproxyPort)

	// --- PREROUTING chain (for forwarded / incoming traffic) ---
	if err := ipt("-t", "mangle", "-N", chainName); err != nil {
		return fmt.Errorf("create chain %s: %w", chainName, err)
	}

	// Exclude private/reserved networks.
	for _, cidr := range privateNetworks {
		if err := ipt("-t", "mangle", "-A", chainName, "-d", cidr, "-j", "RETURN"); err != nil {
			return fmt.Errorf("exclude %s: %w", cidr, err)
		}
	}

	// Exclude the real VPN server IP.
	if m.serverIP != "" {
		if err := ipt("-t", "mangle", "-A", chainName, "-d", m.serverIP, "-j", "RETURN"); err != nil {
			return fmt.Errorf("exclude server %s: %w", m.serverIP, err)
		}
	}

	// TPROXY TCP.
	if err := ipt("-t", "mangle", "-A", chainName,
		"-p", "tcp",
		"-j", "TPROXY",
		"--on-port", port,
		"--tproxy-mark", mark); err != nil {
		return fmt.Errorf("tproxy tcp: %w", err)
	}

	// TPROXY UDP.
	if err := ipt("-t", "mangle", "-A", chainName,
		"-p", "udp",
		"-j", "TPROXY",
		"--on-port", port,
		"--tproxy-mark", mark); err != nil {
		return fmt.Errorf("tproxy udp: %w", err)
	}

	// Jump from PREROUTING to our chain.
	if err := ipt("-t", "mangle", "-A", "PREROUTING", "-j", chainName); err != nil {
		return fmt.Errorf("prerouting jump: %w", err)
	}

	// --- OUTPUT chain (for local traffic) ---
	if err := ipt("-t", "mangle", "-N", markChainName); err != nil {
		return fmt.Errorf("create chain %s: %w", markChainName, err)
	}

	// Exclude private/reserved networks.
	for _, cidr := range privateNetworks {
		if err := ipt("-t", "mangle", "-A", markChainName, "-d", cidr, "-j", "RETURN"); err != nil {
			return fmt.Errorf("mark exclude %s: %w", cidr, err)
		}
	}

	// Exclude the real VPN server IP.
	if m.serverIP != "" {
		if err := ipt("-t", "mangle", "-A", markChainName, "-d", m.serverIP, "-j", "RETURN"); err != nil {
			return fmt.Errorf("mark exclude server: %w", err)
		}
	}

	// Don't redirect xray's own traffic (match by fwmark 255/0xff or non-root UID).
	_ = ipt("-t", "mangle", "-A", markChainName, "-m", "mark", "--mark", "255", "-j", "RETURN")
	_ = ipt("-t", "mangle", "-A", markChainName, "-m", "mark", "--mark", "0xff", "-j", "RETURN")

	if m.xrayUID != "" && m.xrayUID != "0" {
		if err := ipt("-t", "mangle", "-A", markChainName,
			"-m", "owner", "--uid-owner", m.xrayUID, "-j", "RETURN"); err != nil {
			return fmt.Errorf("mark exclude xray uid: %w", err)
		}
	}

	// Mark TCP.
	if err := ipt("-t", "mangle", "-A", markChainName,
		"-p", "tcp", "-j", "MARK", "--set-mark", mark); err != nil {
		return fmt.Errorf("mark tcp: %w", err)
	}

	// Mark UDP.
	if err := ipt("-t", "mangle", "-A", markChainName,
		"-p", "udp", "-j", "MARK", "--set-mark", mark); err != nil {
		return fmt.Errorf("mark udp: %w", err)
	}

	// Jump from OUTPUT to our chain.
	if err := ipt("-t", "mangle", "-A", "OUTPUT", "-j", markChainName); err != nil {
		return fmt.Errorf("output jump: %w", err)
	}

	return nil
}

func (m *Manager) cleanupIPTables() error {
	var firstErr error
	collect := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Remove jumps first.
	collect(iptQuiet("-t", "mangle", "-D", "PREROUTING", "-j", chainName))
	collect(iptQuiet("-t", "mangle", "-D", "OUTPUT", "-j", markChainName))

	// Flush and delete chains.
	collect(iptQuiet("-t", "mangle", "-F", chainName))
	collect(iptQuiet("-t", "mangle", "-X", chainName))
	collect(iptQuiet("-t", "mangle", "-F", markChainName))
	collect(iptQuiet("-t", "mangle", "-X", markChainName))

	return firstErr
}

// ---------- DNS helpers ----------

// currentDNSServers reads the current DNS servers from /etc/resolv.conf.
func currentDNSServers() ([]string, error) {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil, err
	}

	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				servers = append(servers, fields[1])
			}
		}
	}
	return servers, nil
}

// setSystemDNS sets the system DNS using resolvectl on our TUN device.
// In TPROXY mode, UDP port 53 traffic is redirected transparently by iptables.
func (m *Manager) setSystemDNS(dnsAddr string) error {
	_ = runQuiet("resolvectl", "dns", m.tunName, "1.1.1.1", "8.8.8.8")
	_ = runQuiet("resolvectl", "domain", m.tunName, "~.")
	_ = runQuiet("resolvectl", "default-route", m.tunName, "true")
	return nil
}

// restoreSystemDNS restores DNS settings if resolvectl was used.
func (m *Manager) restoreSystemDNS() error {
	_ = runQuiet("resolvectl", "revert", m.tunName)
	return nil
}

// ---------- exec helpers ----------

// run executes a command and returns an error if it fails.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// runQuiet executes a command, suppressing output. Used for cleanup where
// failure is acceptable.
func runQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// ipt is a shorthand for running iptables.
func ipt(args ...string) error {
	return run("iptables", args...)
}

// iptQuiet runs iptables suppressing output (for cleanup).
func iptQuiet(args ...string) error {
	return runQuiet("iptables", args...)
}

// sysctl sets a kernel parameter.
func sysctl(key, value string) error {
	return run("sysctl", "-w", key+"="+value)
}

// resolveIP resolves a hostname to its first IPv4 address.
// If the input is already an IP, it is returned as-is.
func resolveIP(host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return host, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var r net.Resolver
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return "", err
	}

	// Prefer IPv4.
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			return a, nil
		}
	}

	if len(addrs) > 0 {
		return addrs[0], nil
	}
	return "", fmt.Errorf("no addresses found for %s", host)
}
