package subscription

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/notxaked/customv2ray/internal/config"
)

func TestParseJSONResponse(t *testing.T) {
	cfg := config.DefaultConfig()
	mgr := NewManager(cfg)

	// Server returns a list of JSON objects (full Xray configs or outbounds)
	jsonData := `[
		{
			"remarks": "US-VLESS-Fast",
			"log": {"loglevel": "warning"},
			"outbounds": [
				{
					"tag": "proxy",
					"protocol": "vless",
					"settings": {
						"vnext": [
							{
								"address": "us1.vpn.example.com",
								"port": 443,
								"users": [{"id": "uuid-1234", "encryption": "none"}]
							}
						]
					},
					"streamSettings": {
						"network": "ws",
						"security": "tls"
					}
				}
			]
		},
		{
			"tag": "DE-Trojan-Secure",
			"protocol": "trojan",
			"settings": {
				"servers": [
					{
						"address": "de1.vpn.example.com",
						"port": 8443,
						"password": "secret-password"
					}
				]
			}
		},
		{
			"name": "NL-Standard",
			"address": "nl1.vpn.example.com",
			"port": 1080,
			"protocol": "socks",
			"outbound": {
				"protocol": "socks",
				"settings": {
					"servers": [{"address": "nl1.vpn.example.com", "port": 1080}]
				}
			}
		}
	]`

	configs, err := mgr.ParseJSONResponse([]byte(jsonData), "my-sub")
	if err != nil {
		t.Fatalf("ParseJSONResponse failed: %v", err)
	}

	if len(configs) != 3 {
		t.Fatalf("expected 3 configs, got %d", len(configs))
	}

	// Verify server 1
	s1 := configs[0]
	if s1.Name != "US-VLESS-Fast" {
		t.Errorf("expected name 'US-VLESS-Fast', got '%s'", s1.Name)
	}
	if s1.Address != "us1.vpn.example.com" {
		t.Errorf("expected address 'us1.vpn.example.com', got '%s'", s1.Address)
	}
	if s1.Port != 443 {
		t.Errorf("expected port 443, got %d", s1.Port)
	}
	if !strings.HasPrefix(s1.Protocol, "vless") {
		t.Errorf("expected protocol to start with 'vless', got '%s'", s1.Protocol)
	}

	// Verify server 2
	s2 := configs[1]
	if s2.Name != "DE-Trojan-Secure" {
		t.Errorf("expected name 'DE-Trojan-Secure', got '%s'", s2.Name)
	}
	if s2.Address != "de1.vpn.example.com" {
		t.Errorf("expected address 'de1.vpn.example.com', got '%s'", s2.Address)
	}
	if s2.Port != 8443 {
		t.Errorf("expected port 8443, got %d", s2.Port)
	}
	if s2.Protocol != "trojan" {
		t.Errorf("expected protocol 'trojan', got '%s'", s2.Protocol)
	}

	// Verify server 3
	s3 := configs[2]
	if s3.Name != "NL-Standard" {
		t.Errorf("expected name 'NL-Standard', got '%s'", s3.Name)
	}
	if s3.Address != "nl1.vpn.example.com" {
		t.Errorf("expected address 'nl1.vpn.example.com', got '%s'", s3.Address)
	}
	if s3.Port != 1080 {
		t.Errorf("expected port 1080, got %d", s3.Port)
	}
}

func TestFetchSubscription_CustomUserAgent(t *testing.T) {
	var receivedUserAgent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		resp := []map[string]interface{}{
			{
				"name":     "Test-Server",
				"address":  "test.vpn.com",
				"port":     443,
				"protocol": "vless",
				"outbound": map[string]interface{}{
					"protocol": "vless",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	mgr := NewManager(cfg)

	sub := &config.Subscription{
		Name:    "TestSub",
		URL:     server.URL,
		Enabled: true,
	}

	if err := mgr.FetchSubscription(sub); err != nil {
		t.Fatalf("FetchSubscription failed: %v", err)
	}

	// Verify User-Agent matches "Xaked-Prilozhyha" exactly
	expectedUA := "Xaked-Prilozhyha"
	if receivedUserAgent != expectedUA {
		t.Errorf("expected User-Agent '%s', got '%s'", expectedUA, receivedUserAgent)
	}

	if len(sub.Configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(sub.Configs))
	}
}

