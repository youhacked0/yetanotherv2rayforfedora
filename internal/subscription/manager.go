// Package subscription manages fetching, parsing, and storing Xray config
// subscriptions from remote servers.
package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/notxaked/customv2ray/internal/config"
)

// Manager handles subscription fetching, parsing, and auto-updating.
type Manager struct {
	cfg    *config.AppConfig
	client *http.Client
	mu     sync.Mutex
}

// NewManager creates a new subscription manager.
func NewManager(cfg *config.AppConfig) *Manager {
	return &Manager{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// generateID creates a unique deterministic ID from subscription name, address, and port.
func generateID(subName, address string, port int) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s|%s|%d", subName, address, port)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// FetchSubscription fetches a subscription from its URL and updates its configs.
func (m *Manager) FetchSubscription(sub *config.Subscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	req, err := http.NewRequest(http.MethodGet, sub.URL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", config.UserAgent)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch subscription %q: %w", sub.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch subscription %q: HTTP %d", sub.Name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response for %q: %w", sub.Name, err)
	}

	configs, err := m.parseResponse(body, sub.Name)
	if err != nil {
		return fmt.Errorf("parse subscription %q: %w", sub.Name, err)
	}

	sub.Configs = configs
	sub.UpdatedAt = time.Now()

	return nil
}

// parseResponse tries JSON first, then falls back to base64.
func (m *Manager) parseResponse(data []byte, subName string) ([]config.ServerConfig, error) {
	// Try JSON array first.
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) > 0 && trimmed[0] == '[' {
		configs, err := m.ParseJSONResponse(data, subName)
		if err == nil {
			return configs, nil
		}
	}

	// Fall back to base64-encoded URI list.
	return m.ParseBase64Response(data, subName)
}

// subServerJSON is the JSON format for a single server in a subscription response.
type subServerJSON struct {
	Name     string          `json:"name"`
	Address  string          `json:"address"`
	Port     int             `json:"port"`
	Protocol string          `json:"protocol"`
	Outbound json.RawMessage `json:"outbound"`
}

// ParseJSONResponse parses a JSON array subscription response.
func (m *Manager) ParseJSONResponse(data []byte, subName string) ([]config.ServerConfig, error) {
	var rawList []json.RawMessage
	if err := json.Unmarshal(data, &rawList); err != nil {
		return nil, fmt.Errorf("unmarshal JSON response: %w", err)
	}

	configs := make([]config.ServerConfig, 0, len(rawList))
	for i, raw := range rawList {
		cfg, err := parseSingleServerJSON(raw, subName, i)
		if err != nil {
			log.Printf("subscription %q: skip invalid item #%d: %v", subName, i+1, err)
			continue
		}
		configs = append(configs, *cfg)
	}

	return configs, nil
}

func parseSingleServerJSON(raw []byte, subName string, idx int) (*config.ServerConfig, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}

	var name, address, protocol string
	var port int

	if s, ok := m["name"].(string); ok && s != "" {
		name = s
	} else if s, ok := m["remarks"].(string); ok && s != "" {
		name = s
	} else if s, ok := m["ps"].(string); ok && s != "" {
		name = s
	} else if s, ok := m["tag"].(string); ok && s != "" {
		name = s
	}

	if a, ok := m["address"].(string); ok {
		address = a
	}
	if p, ok := m["port"].(float64); ok {
		port = int(p)
	}
	if pr, ok := m["protocol"].(string); ok {
		protocol = pr
	}

	var outboundRaw json.RawMessage = raw
	if ob, ok := m["outbound"]; ok && ob != nil {
		if b, err := json.Marshal(ob); err == nil {
			outboundRaw = b
		}
	}

	var outboundMap map[string]interface{}
	if outboundsRaw, ok := m["outbounds"].([]interface{}); ok && len(outboundsRaw) > 0 {
		if first, ok := outboundsRaw[0].(map[string]interface{}); ok {
			outboundMap = first
		}
	} else if _, ok := m["protocol"].(string); ok {
		outboundMap = m
	}

	if outboundMap != nil {
		if protocol == "" {
			if pr, ok := outboundMap["protocol"].(string); ok {
				protocol = pr
			}
		}
		if name == "" {
			if t, ok := outboundMap["tag"].(string); ok && t != "" {
				name = t
			}
		}
		if settings, ok := outboundMap["settings"].(map[string]interface{}); ok {
			if vnext, ok := settings["vnext"].([]interface{}); ok && len(vnext) > 0 {
				if firstVnext, ok := vnext[0].(map[string]interface{}); ok {
					if address == "" {
						if a, ok := firstVnext["address"].(string); ok {
							address = a
						}
					}
					if port == 0 {
						if p, ok := firstVnext["port"].(float64); ok {
							port = int(p)
						}
					}
				}
			} else if servers, ok := settings["servers"].([]interface{}); ok && len(servers) > 0 {
				if firstServer, ok := servers[0].(map[string]interface{}); ok {
					if address == "" {
						if a, ok := firstServer["address"].(string); ok {
							address = a
						}
					}
					if port == 0 {
						if p, ok := firstServer["port"].(float64); ok {
							port = int(p)
						}
					}
				}
			}
		}
	}

	if name == "" {
		name = fmt.Sprintf("%s #%d", subName, idx+1)
	}
	name = strings.TrimSpace(name)
	if address == "" {
		address = "127.0.0.1"
	}

	id := generateID(subName, address, port)
	if id == "" || len(id) < 8 {
		h := sha256.Sum256(raw)
		id = fmt.Sprintf("%x", h[:8])
	}

	sc := &config.ServerConfig{
		ID:       id,
		Name:     name,
		Address:  address,
		Port:     port,
		Protocol: protocol,
		SubName:  subName,
		RawJSON:  outboundRaw,
	}
	sc.Protocol = sc.FullProtocol()
	return sc, nil
}

// ParseBase64Response decodes base64 content and parses each line as a protocol URI.
func (m *Manager) ParseBase64Response(data []byte, subName string) ([]config.ServerConfig, error) {
	// Try standard base64 first, then URL-safe base64.
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			// Maybe it's already plain text URIs.
			if utf8.Valid(data) && looksLikeURIList(string(data)) {
				decoded = data
			} else {
				return nil, fmt.Errorf("decode base64: %w", err)
			}
		}
	}

	lines := strings.Split(strings.TrimSpace(string(decoded)), "\n")
	var configs []config.ServerConfig

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var sc *config.ServerConfig

		switch {
		case strings.HasPrefix(line, "vmess://"):
			sc, err = m.ParseVmessURI(line, subName)
		case strings.HasPrefix(line, "vless://"):
			sc, err = m.ParseVlessURI(line, subName)
		case strings.HasPrefix(line, "trojan://"):
			sc, err = m.ParseTrojanURI(line, subName)
		case strings.HasPrefix(line, "ss://"):
			sc, err = m.ParseSSURI(line, subName)
		default:
			continue
		}

		if err != nil {
			log.Printf("warning: skip invalid URI in %q: %v", subName, err)
			continue
		}
		if sc != nil {
			configs = append(configs, *sc)
		}
	}

	if len(configs) == 0 {
		return nil, fmt.Errorf("no valid configs found in subscription %q", subName)
	}

	return configs, nil
}

// looksLikeURIList checks if data looks like protocol URIs.
func looksLikeURIList(s string) bool {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "vmess://") ||
			strings.HasPrefix(line, "vless://") ||
			strings.HasPrefix(line, "trojan://") ||
			strings.HasPrefix(line, "ss://") {
			return true
		}
	}
	return false
}

// vmessJSON is the JSON structure inside a vmess:// URI.
type vmessJSON struct {
	V    string      `json:"v"`
	PS   string      `json:"ps"`
	Add  string      `json:"add"`
	Port interface{} `json:"port"` // can be string or number
	ID   string      `json:"id"`
	AID  interface{} `json:"aid"` // can be string or number
	Net  string      `json:"net"`
	Type string      `json:"type"`
	Host string      `json:"host"`
	Path string      `json:"path"`
	TLS  string      `json:"tls"`
	SNI  string      `json:"sni"`
	ALPN string      `json:"alpn"`
	FP   string      `json:"fp"`
}

// ParseVmessURI parses a vmess:// URI into a ServerConfig.
func (m *Manager) ParseVmessURI(uri string, subName string) (*config.ServerConfig, error) {
	raw := strings.TrimPrefix(uri, "vmess://")
	raw = strings.TrimSpace(raw)

	// Try standard base64, then raw (no padding).
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
		if err != nil {
			decoded, err = base64.URLEncoding.DecodeString(raw)
			if err != nil {
				decoded, err = base64.RawURLEncoding.DecodeString(raw)
				if err != nil {
					return nil, fmt.Errorf("decode vmess base64: %w", err)
				}
			}
		}
	}

	var v vmessJSON
	if err := json.Unmarshal(decoded, &v); err != nil {
		return nil, fmt.Errorf("unmarshal vmess JSON: %w", err)
	}

	port := parsePort(v.Port)
	if v.Add == "" || port == 0 {
		return nil, fmt.Errorf("vmess: missing address or port")
	}

	alterId := 0
	if v.AID != nil {
		alterId = parsePort(v.AID)
	}

	name := v.PS
	if name == "" {
		name = fmt.Sprintf("%s:%d", v.Add, port)
	}

	// Build xray outbound config.
	outbound := buildVmessOutbound(v, port, alterId)
	raw2, err := json.Marshal(outbound)
	if err != nil {
		return nil, fmt.Errorf("marshal vmess outbound: %w", err)
	}

	return &config.ServerConfig{
		ID:       generateID(subName, v.Add, port),
		Name:     name,
		Address:  v.Add,
		Port:     port,
		Protocol: "vmess",
		SubName:  subName,
		RawJSON:  json.RawMessage(raw2),
	}, nil
}

// buildVmessOutbound constructs the xray outbound config for vmess.
func buildVmessOutbound(v vmessJSON, port, alterId int) map[string]interface{} {
	user := map[string]interface{}{
		"id":       v.ID,
		"alterId":  alterId,
		"security": "auto",
	}

	settings := map[string]interface{}{
		"vnext": []map[string]interface{}{
			{
				"address": v.Add,
				"port":    port,
				"users":   []map[string]interface{}{user},
			},
		},
	}

	network := v.Net
	if network == "" {
		network = "tcp"
	}

	streamSettings := map[string]interface{}{
		"network": network,
	}

	// TLS settings.
	if v.TLS == "tls" {
		tlsSettings := map[string]interface{}{}
		if v.SNI != "" {
			tlsSettings["serverName"] = v.SNI
		}
		if v.FP != "" {
			tlsSettings["fingerprint"] = v.FP
		}
		if v.ALPN != "" {
			tlsSettings["alpn"] = strings.Split(v.ALPN, ",")
		}
		streamSettings["security"] = "tls"
		streamSettings["tlsSettings"] = tlsSettings
	}

	// Network-specific settings.
	switch network {
	case "ws":
		wsSettings := map[string]interface{}{}
		if v.Path != "" {
			wsSettings["path"] = v.Path
		}
		if v.Host != "" {
			wsSettings["headers"] = map[string]string{"Host": v.Host}
		}
		streamSettings["wsSettings"] = wsSettings
	case "grpc":
		grpcSettings := map[string]interface{}{}
		if v.Path != "" {
			grpcSettings["serviceName"] = v.Path
		}
		streamSettings["grpcSettings"] = grpcSettings
	case "tcp":
		if v.Type == "http" {
			tcpSettings := map[string]interface{}{
				"header": map[string]interface{}{
					"type": "http",
					"request": map[string]interface{}{
						"path": []string{v.Path},
						"headers": map[string]interface{}{
							"Host": []string{v.Host},
						},
					},
				},
			}
			streamSettings["tcpSettings"] = tcpSettings
		}
	case "h2":
		h2Settings := map[string]interface{}{}
		if v.Path != "" {
			h2Settings["path"] = v.Path
		}
		if v.Host != "" {
			h2Settings["host"] = strings.Split(v.Host, ",")
		}
		streamSettings["httpSettings"] = h2Settings
	}

	return map[string]interface{}{
		"protocol":       "vmess",
		"settings":       settings,
		"streamSettings": streamSettings,
	}
}

// ParseVlessURI parses a vless:// URI into a ServerConfig.
func (m *Manager) ParseVlessURI(uri string, subName string) (*config.ServerConfig, error) {
	// vless://uuid@host:port?params#name
	raw := strings.TrimPrefix(uri, "vless://")

	// Extract fragment (name).
	name := ""
	if idx := strings.LastIndex(raw, "#"); idx != -1 {
		name, _ = url.PathUnescape(raw[idx+1:])
		raw = raw[:idx]
	}

	// Split into userinfo@host:port and query.
	queryStr := ""
	if idx := strings.Index(raw, "?"); idx != -1 {
		queryStr = raw[idx+1:]
		raw = raw[:idx]
	}

	// Parse userinfo@host:port.
	atIdx := strings.Index(raw, "@")
	if atIdx == -1 {
		return nil, fmt.Errorf("vless: missing @ in URI")
	}
	uuid := raw[:atIdx]
	hostPort := raw[atIdx+1:]

	host, port, err := parseHostPort(hostPort)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}

	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}

	params, _ := url.ParseQuery(queryStr)

	// Build xray outbound.
	outbound := buildVlessOutbound(uuid, host, port, params)
	raw2, err := json.Marshal(outbound)
	if err != nil {
		return nil, fmt.Errorf("marshal vless outbound: %w", err)
	}

	return &config.ServerConfig{
		ID:       generateID(subName, host, port),
		Name:     name,
		Address:  host,
		Port:     port,
		Protocol: "vless",
		SubName:  subName,
		RawJSON:  json.RawMessage(raw2),
	}, nil
}

// buildVlessOutbound constructs the xray outbound config for vless.
func buildVlessOutbound(uuid, host string, port int, params url.Values) map[string]interface{} {
	user := map[string]interface{}{
		"id":         uuid,
		"encryption": "none",
	}

	flow := params.Get("flow")
	if flow != "" {
		user["flow"] = flow
	}

	settings := map[string]interface{}{
		"vnext": []map[string]interface{}{
			{
				"address": host,
				"port":    port,
				"users":   []map[string]interface{}{user},
			},
		},
	}

	network := params.Get("type")
	if network == "" {
		network = "tcp"
	}

	streamSettings := map[string]interface{}{
		"network": network,
	}

	security := params.Get("security")
	if security == "" {
		security = "none"
	}
	streamSettings["security"] = security

	switch security {
	case "tls":
		tlsSettings := map[string]interface{}{}
		if sni := params.Get("sni"); sni != "" {
			tlsSettings["serverName"] = sni
		}
		if fp := params.Get("fp"); fp != "" {
			tlsSettings["fingerprint"] = fp
		}
		if alpn := params.Get("alpn"); alpn != "" {
			tlsSettings["alpn"] = strings.Split(alpn, ",")
		}
		streamSettings["tlsSettings"] = tlsSettings
	case "reality":
		realitySettings := map[string]interface{}{}
		if sni := params.Get("sni"); sni != "" {
			realitySettings["serverName"] = sni
		}
		if fp := params.Get("fp"); fp != "" {
			realitySettings["fingerprint"] = fp
		}
		if pbk := params.Get("pbk"); pbk != "" {
			realitySettings["publicKey"] = pbk
		}
		if sid := params.Get("sid"); sid != "" {
			realitySettings["shortId"] = sid
		}
		if spx := params.Get("spx"); spx != "" {
			realitySettings["spiderX"] = spx
		}
		streamSettings["realitySettings"] = realitySettings
	}

	// Network-specific settings.
	applyNetworkSettings(streamSettings, network, params)

	return map[string]interface{}{
		"protocol":       "vless",
		"settings":       settings,
		"streamSettings": streamSettings,
	}
}

// applyNetworkSettings adds network-specific transport settings.
func applyNetworkSettings(streamSettings map[string]interface{}, network string, params url.Values) {
	switch network {
	case "ws":
		wsSettings := map[string]interface{}{}
		if path := params.Get("path"); path != "" {
			wsSettings["path"] = path
		}
		if host := params.Get("host"); host != "" {
			wsSettings["headers"] = map[string]string{"Host": host}
		}
		streamSettings["wsSettings"] = wsSettings
	case "grpc":
		grpcSettings := map[string]interface{}{}
		if sn := params.Get("serviceName"); sn != "" {
			grpcSettings["serviceName"] = sn
		}
		if mode := params.Get("mode"); mode != "" {
			grpcSettings["mode"] = mode
		}
		streamSettings["grpcSettings"] = grpcSettings
	case "tcp":
		headerType := params.Get("headerType")
		if headerType == "http" {
			tcpSettings := map[string]interface{}{
				"header": map[string]interface{}{
					"type": "http",
					"request": map[string]interface{}{
						"path": []string{params.Get("path")},
						"headers": map[string]interface{}{
							"Host": []string{params.Get("host")},
						},
					},
				},
			}
			streamSettings["tcpSettings"] = tcpSettings
		}
	case "h2", "http":
		h2Settings := map[string]interface{}{}
		if path := params.Get("path"); path != "" {
			h2Settings["path"] = path
		}
		if host := params.Get("host"); host != "" {
			h2Settings["host"] = strings.Split(host, ",")
		}
		streamSettings["httpSettings"] = h2Settings
	case "kcp", "mkcp":
		kcpSettings := map[string]interface{}{}
		if seed := params.Get("seed"); seed != "" {
			kcpSettings["seed"] = seed
		}
		if headerType := params.Get("headerType"); headerType != "" {
			kcpSettings["header"] = map[string]string{"type": headerType}
		}
		streamSettings["kcpSettings"] = kcpSettings
	}
}

// ParseTrojanURI parses a trojan:// URI into a ServerConfig.
func (m *Manager) ParseTrojanURI(uri string, subName string) (*config.ServerConfig, error) {
	// trojan://password@host:port?params#name
	raw := strings.TrimPrefix(uri, "trojan://")

	// Extract fragment (name).
	name := ""
	if idx := strings.LastIndex(raw, "#"); idx != -1 {
		name, _ = url.PathUnescape(raw[idx+1:])
		raw = raw[:idx]
	}

	// Split query.
	queryStr := ""
	if idx := strings.Index(raw, "?"); idx != -1 {
		queryStr = raw[idx+1:]
		raw = raw[:idx]
	}

	// Parse password@host:port.
	atIdx := strings.LastIndex(raw, "@")
	if atIdx == -1 {
		return nil, fmt.Errorf("trojan: missing @ in URI")
	}
	password, _ := url.PathUnescape(raw[:atIdx])
	hostPort := raw[atIdx+1:]

	host, port, err := parseHostPort(hostPort)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}

	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}

	params, _ := url.ParseQuery(queryStr)

	// Build xray outbound.
	outbound := buildTrojanOutbound(password, host, port, params)
	raw2, err := json.Marshal(outbound)
	if err != nil {
		return nil, fmt.Errorf("marshal trojan outbound: %w", err)
	}

	return &config.ServerConfig{
		ID:       generateID(subName, host, port),
		Name:     name,
		Address:  host,
		Port:     port,
		Protocol: "trojan",
		SubName:  subName,
		RawJSON:  json.RawMessage(raw2),
	}, nil
}

// buildTrojanOutbound constructs the xray outbound config for trojan.
func buildTrojanOutbound(password, host string, port int, params url.Values) map[string]interface{} {
	settings := map[string]interface{}{
		"servers": []map[string]interface{}{
			{
				"address":  host,
				"port":     port,
				"password": password,
			},
		},
	}

	network := params.Get("type")
	if network == "" {
		network = "tcp"
	}

	streamSettings := map[string]interface{}{
		"network": network,
	}

	security := params.Get("security")
	if security == "" {
		security = "tls"
	}
	streamSettings["security"] = security

	if security == "tls" {
		tlsSettings := map[string]interface{}{}
		if sni := params.Get("sni"); sni != "" {
			tlsSettings["serverName"] = sni
		}
		if fp := params.Get("fp"); fp != "" {
			tlsSettings["fingerprint"] = fp
		}
		if alpn := params.Get("alpn"); alpn != "" {
			tlsSettings["alpn"] = strings.Split(alpn, ",")
		}
		streamSettings["tlsSettings"] = tlsSettings
	}

	applyNetworkSettings(streamSettings, network, params)

	return map[string]interface{}{
		"protocol":       "trojan",
		"settings":       settings,
		"streamSettings": streamSettings,
	}
}

// ParseSSURI parses an ss:// (Shadowsocks) URI into a ServerConfig.
func (m *Manager) ParseSSURI(uri string, subName string) (*config.ServerConfig, error) {
	// ss://base64(method:password)@host:port#name
	// or ss://base64(method:password@host:port)#name (SIP002)
	raw := strings.TrimPrefix(uri, "ss://")

	// Extract fragment (name).
	name := ""
	if idx := strings.LastIndex(raw, "#"); idx != -1 {
		name, _ = url.PathUnescape(raw[idx+1:])
		raw = raw[:idx]
	}

	var method, password, host string
	var port int

	if atIdx := strings.LastIndex(raw, "@"); atIdx != -1 {
		// SIP002 format: base64(method:password)@host:port
		userInfo := raw[:atIdx]
		hostPort := raw[atIdx+1:]

		decoded, err := decodeBase64Flexible(userInfo)
		if err != nil {
			// Maybe userInfo is already plaintext method:password.
			decoded = []byte(userInfo)
		}

		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("ss: invalid userinfo format")
		}
		method = parts[0]
		password = parts[1]

		host, port, err = parseHostPort(hostPort)
		if err != nil {
			return nil, fmt.Errorf("ss: %w", err)
		}
	} else {
		// Legacy format: base64(method:password@host:port)
		decoded, err := decodeBase64Flexible(raw)
		if err != nil {
			return nil, fmt.Errorf("ss: decode base64: %w", err)
		}

		atIdx2 := strings.LastIndex(string(decoded), "@")
		if atIdx2 == -1 {
			return nil, fmt.Errorf("ss: missing @ in decoded URI")
		}

		parts := strings.SplitN(string(decoded[:atIdx2]), ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("ss: invalid userinfo format")
		}
		method = parts[0]
		password = parts[1]

		host, port, err = parseHostPort(string(decoded[atIdx2+1:]))
		if err != nil {
			return nil, fmt.Errorf("ss: %w", err)
		}
	}

	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}

	// Build xray outbound.
	outbound := buildSSOutbound(method, password, host, port)
	raw2, err := json.Marshal(outbound)
	if err != nil {
		return nil, fmt.Errorf("marshal ss outbound: %w", err)
	}

	return &config.ServerConfig{
		ID:       generateID(subName, host, port),
		Name:     name,
		Address:  host,
		Port:     port,
		Protocol: "shadowsocks",
		SubName:  subName,
		RawJSON:  json.RawMessage(raw2),
	}, nil
}

// buildSSOutbound constructs the xray outbound config for shadowsocks.
func buildSSOutbound(method, password, host string, port int) map[string]interface{} {
	settings := map[string]interface{}{
		"servers": []map[string]interface{}{
			{
				"address":  host,
				"port":     port,
				"method":   method,
				"password": password,
			},
		},
	}

	streamSettings := map[string]interface{}{
		"network": "tcp",
	}

	return map[string]interface{}{
		"protocol":       "shadowsocks",
		"settings":       settings,
		"streamSettings": streamSettings,
	}
}

// UpdateAll fetches and updates all enabled subscriptions.
func (m *Manager) UpdateAll() error {
	cfg := m.cfg
	var errs []string

	for i := range cfg.Subscriptions {
		sub := &cfg.Subscriptions[i]
		if !sub.Enabled {
			continue
		}

		if err := m.FetchSubscription(sub); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", sub.Name, err))
			continue
		}
		log.Printf("updated subscription %q: %d configs", sub.Name, len(sub.Configs))
	}

	if err := cfg.Save(); err != nil {
		errs = append(errs, fmt.Sprintf("save config: %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("update errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// StartAutoUpdate starts a background goroutine that refreshes subscriptions
// at the interval configured in AppConfig.UpdateInterval (minutes).
// It blocks until ctx is cancelled.
func (m *Manager) StartAutoUpdate(ctx context.Context) {
	interval := time.Duration(m.cfg.UpdateInterval) * time.Minute
	if interval <= 0 {
		interval = 60 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("auto-updater started (interval: %v)", interval)

	for {
		select {
		case <-ctx.Done():
			log.Println("auto-updater stopped")
			return
		case <-ticker.C:
			if !m.cfg.AutoUpdate {
				continue
			}
			log.Println("auto-updater: refreshing subscriptions")
			if err := m.UpdateAll(); err != nil {
				log.Printf("auto-updater: %v", err)
			}
		}
	}
}

// parseHostPort parses "host:port" or "[ipv6]:port" into host and port.
func parseHostPort(s string) (string, int, error) {
	s = strings.TrimSpace(s)

	// Handle IPv6 brackets: [::1]:port
	if strings.HasPrefix(s, "[") {
		closeBracket := strings.Index(s, "]")
		if closeBracket == -1 {
			return "", 0, fmt.Errorf("invalid IPv6 address: missing ]")
		}
		host := s[1:closeBracket]
		rest := s[closeBracket+1:]
		if !strings.HasPrefix(rest, ":") {
			return "", 0, fmt.Errorf("missing port after IPv6 address")
		}
		port, err := strconv.Atoi(rest[1:])
		if err != nil {
			return "", 0, fmt.Errorf("invalid port: %w", err)
		}
		return host, port, nil
	}

	// Regular host:port - find the last colon.
	lastColon := strings.LastIndex(s, ":")
	if lastColon == -1 {
		return "", 0, fmt.Errorf("missing port in %q", s)
	}

	host := s[:lastColon]
	port, err := strconv.Atoi(s[lastColon+1:])
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %w", err)
	}
	return host, port, nil
}

// parsePort converts a port value that might be a string or number to int.
func parsePort(v interface{}) int {
	switch p := v.(type) {
	case float64:
		return int(p)
	case int:
		return p
	case string:
		n, _ := strconv.Atoi(p)
		return n
	default:
		return 0
	}
}

// decodeBase64Flexible tries multiple base64 encodings.
func decodeBase64Flexible(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(s)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("failed to decode base64")
}
