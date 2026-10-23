// Package xray manages xray-core process lifecycle and configuration building.
package xray

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/notxaked/customv2ray/internal/config"
)

// findFreePort checks if the preferred port is available on 127.0.0.1;
// if busy (e.g. by another active VPN), it finds an alternate available port.
func findFreePort(preferred int) int {
	if preferred <= 0 {
		preferred = config.DefaultSOCKS
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferred))
	if err == nil {
		_ = ln.Close()
		return preferred
	}

	for _, offset := range []int{10000, 20000, 100, 200, 500, 1000} {
		candidate := preferred + offset
		if candidate < 65535 {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", candidate))
			if err == nil {
				_ = ln.Close()
				return candidate
			}
		}
	}

	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		p := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		return p
	}
	return preferred
}

// xrayConfigDir returns the directory for generated xray configs.
func xrayConfigDir() string {
	return filepath.Join(config.ConfigDir(), "xray")
}

// CurrentConfigPath returns the path to the active xray config file.
func CurrentConfigPath() string {
	return filepath.Join(xrayConfigDir(), "current.json")
}

// BuildConfig creates a complete xray configuration JSON from app settings
// and a server config. The resulting JSON includes log, dns, inbounds,
// outbounds, and routing sections ready for xray-core consumption.
func BuildConfig(appCfg *config.AppConfig, server *config.ServerConfig) ([]byte, error) {
	if server == nil {
		return nil, fmt.Errorf("server config is nil")
	}
	if len(server.RawJSON) == 0 {
		return nil, fmt.Errorf("server %q has empty RawJSON", server.Name)
	}

	// Case 1: The server returned a full, self-contained Xray configuration JSON!
	// In this case, we preserve the server's routing rules, DNS, and outbounds,
	// and inject our local inbounds (socks-in, http-in, tproxy-in).
	var root map[string]interface{}
	if err := json.Unmarshal(server.RawJSON, &root); err == nil && root["outbounds"] != nil {
		logLevel := appCfg.LogLevel
		if logLevel == "" || logLevel == "warning" {
			logLevel = "info"
		}
		root["log"] = map[string]interface{}{"loglevel": logLevel}

		socksPort := findFreePort(appCfg.SOCKSPort)
		httpPort := findFreePort(appCfg.HTTPPort)
		tproxyPort := findFreePort(appCfg.TProxyPort)
		appCfg.SOCKSPort = socksPort
		appCfg.HTTPPort = httpPort
		appCfg.TProxyPort = tproxyPort

		inboundsRaw, _ := root["inbounds"].([]interface{})
		var inbounds []map[string]interface{}
		hasSocks := false
		hasHTTP := false
		hasTProxy := false

		for _, item := range inboundsRaw {
			if m, ok := item.(map[string]interface{}); ok {
				proto, _ := m["protocol"].(string)
				tag, _ := m["tag"].(string)
				if proto == "socks" || tag == "socks" || tag == "socks-in" {
					hasSocks = true
					m["port"] = socksPort
				} else if proto == "http" || tag == "http" || tag == "http-in" {
					hasHTTP = true
					m["port"] = httpPort
				} else if tag == "tproxy-in" || tag == "tproxy" {
					hasTProxy = true
					m["port"] = tproxyPort
					m["listen"] = "0.0.0.0"
				}
				inbounds = append(inbounds, m)
			}
		}

		if !hasSocks {
			inbounds = append(inbounds, map[string]interface{}{
				"tag":      "socks-in",
				"port":     socksPort,
				"listen":   "127.0.0.1",
				"protocol": "socks",
				"settings": map[string]interface{}{
					"auth": "noauth",
					"udp":  true,
				},
				"sniffing": map[string]interface{}{
					"enabled":      true,
					"destOverride": []string{"http", "tls", "quic"},
				},
			})
		}

		if !hasHTTP {
			inbounds = append(inbounds, map[string]interface{}{
				"tag":      "http-in",
				"port":     httpPort,
				"listen":   "127.0.0.1",
				"protocol": "http",
				"settings": map[string]interface{}{},
				"sniffing": map[string]interface{}{
					"enabled":      true,
					"destOverride": []string{"http", "tls", "quic"},
				},
			})
		}

		if !hasTProxy && (appCfg.Mode == config.ModeTUN || appCfg.Mode == config.ModeBoth) {
			inbounds = append(inbounds, map[string]interface{}{
				"tag":      "tproxy-in",
				"port":     tproxyPort,
				"listen":   "0.0.0.0",
				"protocol": "dokodemo-door",
				"settings": map[string]interface{}{
					"network":        "tcp,udp",
					"followRedirect": true,
				},
				"streamSettings": map[string]interface{}{
					"sockopt": map[string]interface{}{
						"tproxy": "tproxy",
					},
				},
				"sniffing": map[string]interface{}{
					"enabled":      true,
					"destOverride": []string{"http", "tls", "quic"},
				},
			})
		}

		root["inbounds"] = inbounds

		// Ensure sockopt.mark = 255 on outbounds so Xray outbound traffic bypasses TPROXY
		if outboundsRaw, ok := root["outbounds"].([]interface{}); ok {
			for _, ob := range outboundsRaw {
				if obMap, ok := ob.(map[string]interface{}); ok {
					proto, _ := obMap["protocol"].(string)
					if proto == "freedom" || proto == "vless" || proto == "vmess" || proto == "trojan" || proto == "shadowsocks" {
						ss, _ := obMap["streamSettings"].(map[string]interface{})
						if ss == nil {
							ss = make(map[string]interface{})
						}
						sockopt, _ := ss["sockopt"].(map[string]interface{})
						if sockopt == nil {
							sockopt = make(map[string]interface{})
						}
						sockopt["mark"] = 255
						ss["sockopt"] = sockopt
						obMap["streamSettings"] = ss
					}
				}
			}
		}

		return json.MarshalIndent(root, "", "  ")
	}

	// Case 2: Single outbound object fallback
	proxyOutbound, err := extractProxyOutbound(server.RawJSON)
	if err != nil {
		return nil, fmt.Errorf("extract proxy outbound for %q: %w", server.Name, err)
	}

	// Ensure the proxy outbound has tag "proxy".
	proxyOutbound["tag"] = "proxy"

	logLevel := appCfg.LogLevel
	if logLevel == "" || logLevel == "warning" {
		logLevel = "info"
	}

	fullConfig := map[string]interface{}{
		"log": map[string]interface{}{
			"loglevel": logLevel,
		},
		"dns":       BuildDNS(),
		"inbounds":  BuildInbounds(appCfg),
		"outbounds": buildOutbounds(proxyOutbound),
		"routing":   BuildRouting(),
	}

	data, err := json.MarshalIndent(fullConfig, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal xray config: %w", err)
	}

	return data, nil
}

// WriteConfig builds the xray config and writes it to the standard config path.
// Returns the file path that was written to.
func WriteConfig(appCfg *config.AppConfig, server *config.ServerConfig) (string, error) {
	data, err := BuildConfig(appCfg, server)
	if err != nil {
		return "", err
	}

	dir := xrayConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create xray config dir: %w", err)
	}

	path := CurrentConfigPath()
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write xray config: %w", err)
	}
	_ = os.Chmod(path, 0644)

	return path, nil
}

// BuildInbounds creates the inbound proxy listeners based on app configuration.
// It always creates SOCKS5 and HTTP inbounds, and optionally a dokodemo-door
// inbound for DNS interception if DNSPort is configured.
func BuildInbounds(appCfg *config.AppConfig) []map[string]interface{} {
	socksPort := findFreePort(appCfg.SOCKSPort)
	httpPort := findFreePort(appCfg.HTTPPort)
	appCfg.SOCKSPort = socksPort
	appCfg.HTTPPort = httpPort

	inbounds := []map[string]interface{}{
		{
			"tag":      "socks-in",
			"port":     socksPort,
			"listen":   "127.0.0.1",
			"protocol": "socks",
			"settings": map[string]interface{}{
				"auth": "noauth",
				"udp":  true,
			},
			"sniffing": map[string]interface{}{
				"enabled":      true,
				"destOverride": []string{"http", "tls"},
			},
		},
		{
			"tag":      "http-in",
			"port":     httpPort,
			"listen":   "127.0.0.1",
			"protocol": "http",
			"settings": map[string]interface{}{},
			"sniffing": map[string]interface{}{
				"enabled":      true,
				"destOverride": []string{"http", "tls"},
			},
		},
	}

	// Add TPROXY inbound for transparent proxying (TUN mode).
	tproxyPort := appCfg.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = config.DefaultTProxy
	}
	inbounds = append(inbounds, map[string]interface{}{
		"tag":      "tproxy-in",
		"port":     tproxyPort,
		"listen":   "127.0.0.1",
		"protocol": "dokodemo-door",
		"settings": map[string]interface{}{
			"network":        "tcp,udp",
			"followRedirect": true,
		},
		"streamSettings": map[string]interface{}{
			"sockopt": map[string]interface{}{
				"tproxy": "tproxy",
			},
		},
		"sniffing": map[string]interface{}{
			"enabled":      true,
			"destOverride": []string{"http", "tls", "quic"},
		},
	})

	// Add DNS inbound if DNSPort is configured.
	dnsPort := appCfg.DNSPort
	if dnsPort > 0 {
		inbounds = append(inbounds, map[string]interface{}{
			"tag":      "dns-in",
			"port":     dnsPort,
			"listen":   "127.0.0.1",
			"protocol": "dokodemo-door",
			"settings": map[string]interface{}{
				"address": "1.1.1.1",
				"port":    53,
				"network": "tcp,udp",
			},
		})
	}

	return inbounds
}

// BuildDNS creates the DNS configuration section using DNS-over-HTTPS
// with Cloudflare and Google resolvers.
func BuildDNS() map[string]interface{} {
	return map[string]interface{}{
		"servers": []interface{}{
			map[string]interface{}{
				"address": "https://1.1.1.1/dns-query",
				"domains": []string{"geosite:geolocation-!cn"},
			},
			map[string]interface{}{
				"address": "1.1.1.1",
				"port":    53,
			},
			map[string]interface{}{
				"address": "8.8.8.8",
				"port":    53,
			},
		},
	}
}

// BuildRouting creates the routing rules section. Traffic is routed as follows:
//   - Private IPs → direct
//   - Advertising domains → block
//   - All other TCP/UDP traffic → proxy
func BuildRouting() map[string]interface{} {
	return map[string]interface{}{
		"domainStrategy": "AsIs",
		"rules": []map[string]interface{}{
			{
				"type":        "field",
				"outboundTag": "dns-out",
				"inboundTag":  []string{"dns-in"},
			},
			{
				"type":        "field",
				"outboundTag": "direct",
				"ip":          []string{"geoip:private"},
			},
			{
				"type":        "field",
				"outboundTag": "block",
				"domain":      []string{"geosite:category-ads-all"},
			},
			{
				"type":        "field",
				"outboundTag": "proxy",
				"inboundTag":  []string{"tproxy-in", "socks-in", "http-in"},
			},
			{
				"type":        "field",
				"outboundTag": "proxy",
				"network":     "tcp,udp",
			},
		},
	}
}

// extractProxyOutbound determines the type of RawJSON and extracts the
// proxy outbound configuration. It handles two cases:
//   - Full xray config with "outbounds" array → extracts the first outbound
//   - Single outbound object with "protocol" key → uses directly
func extractProxyOutbound(raw json.RawMessage) (map[string]interface{}, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("unmarshal RawJSON: %w", err)
	}

	// Case 1: Full xray config with "outbounds" array.
	if outboundsRaw, ok := obj["outbounds"]; ok {
		outbounds, ok := outboundsRaw.([]interface{})
		if !ok || len(outbounds) == 0 {
			return nil, fmt.Errorf("outbounds is not a valid array or is empty")
		}
		first, ok := outbounds[0].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("first outbound is not a valid object")
		}
		return first, nil
	}

	// Case 2: Single outbound object with "protocol" key.
	if _, ok := obj["protocol"]; ok {
		return obj, nil
	}

	return nil, fmt.Errorf("RawJSON is neither a full xray config nor a single outbound object")
}

// buildOutbounds creates the complete outbounds array with the proxy outbound
// plus the standard "direct" and "block" outbounds.
func buildOutbounds(proxyOutbound map[string]interface{}) []map[string]interface{} {
	return []map[string]interface{}{
		proxyOutbound,
		{
			"protocol": "freedom",
			"tag":      "direct",
		},
		{
			"protocol": "blackhole",
			"tag":      "block",
		},
		{
			"protocol": "dns",
			"tag":      "dns-out",
		},
	}
}
