package xray

import (
	"encoding/json"
	"testing"

	"github.com/notxaked/customv2ray/internal/config"
)

func TestBuildConfig(t *testing.T) {
	appCfg := config.DefaultConfig()
	appCfg.SOCKSPort = 10808
	appCfg.HTTPPort = 10809
	appCfg.DNSPort = 5353

	serverCfg := &config.ServerConfig{
		ID:       "test-id",
		Name:     "Test Server",
		Address:  "example.com",
		Port:     443,
		Protocol: "vless",
		RawJSON: []byte(`{
			"tag": "proxy",
			"protocol": "vless",
			"settings": {
				"vnext": [{"address": "example.com", "port": 443}]
			}
		}`),
	}

	data, err := BuildConfig(appCfg, serverCfg)
	if err != nil {
		t.Fatalf("BuildConfig failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal generated config failed: %v", err)
	}

	// Verify inbounds
	inbounds, ok := parsed["inbounds"].([]interface{})
	if !ok || len(inbounds) < 3 {
		t.Fatalf("expected at least 3 inbounds (socks, http, dns), got %d", len(inbounds))
	}

	// Verify outbounds
	outbounds, ok := parsed["outbounds"].([]interface{})
	if !ok || len(outbounds) < 3 {
		t.Fatalf("expected outbounds, got %d", len(outbounds))
	}

	// First outbound should be tag "proxy"
	firstOB, ok := outbounds[0].(map[string]interface{})
	if !ok || firstOB["tag"] != "proxy" {
		t.Errorf("expected first outbound tag 'proxy', got %v", firstOB["tag"])
	}

	// Verify routing
	routing, ok := parsed["routing"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing routing block")
	}
	rules, ok := routing["rules"].([]interface{})
	if !ok || len(rules) == 0 {
		t.Fatalf("missing routing rules")
	}
}

func TestBuildConfig_FullServerConfig(t *testing.T) {
	appCfg := config.DefaultConfig()
	appCfg.Mode = config.ModeTUN

	serverCfg := &config.ServerConfig{
		ID:       "poland-id",
		Name:     "🇵🇱 Польша",
		Address:  "vpn.example.com",
		Port:     443,
		Protocol: "vless",
		RawJSON: []byte(`{
			"dns": {"servers": ["1.1.1.1"]},
			"routing": {
				"rules": [
					{"outboundTag": "direct", "domain": ["domain:ru"]}
				]
			},
			"outbounds": [
				{
					"tag": "proxy",
					"protocol": "vless",
					"settings": {"vnext": [{"address": "vpn.example.com", "port": 443}]}
				},
				{"tag": "direct", "protocol": "freedom"}
			]
		}`),
	}

	data, err := BuildConfig(appCfg, serverCfg)
	if err != nil {
		t.Fatalf("BuildConfig failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify server routing was preserved!
	routing := parsed["routing"].(map[string]interface{})
	rules := routing["rules"].([]interface{})
	if len(rules) != 1 {
		t.Errorf("expected server routing preserved, got %d rules", len(rules))
	}

	// Verify inbounds has tproxy-in
	inbounds := parsed["inbounds"].([]interface{})
	hasTProxy := false
	for _, ib := range inbounds {
		m := ib.(map[string]interface{})
		if m["tag"] == "tproxy-in" {
			hasTProxy = true
			break
		}
	}
	if !hasTProxy {
		t.Errorf("expected tproxy-in inbound added for TUN mode")
	}
}

