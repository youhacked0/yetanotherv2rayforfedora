package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/notxaked/customv2ray/internal/config"
)

func TestDaemonLifecycleAndAPI(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("CUSTOMV2RAY_CONFIG_DIR", tmpDir)
	t.Setenv("CUSTOMV2RAY_DATA_DIR", tmpDir)

	sockPath := filepath.Join(tmpDir, "test.sock")

	// Mock subscription server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != config.UserAgent {
			http.Error(w, "bad user agent", http.StatusForbidden)
			return
		}
		data := []map[string]interface{}{
			{
				"name":     "Fast-Node",
				"address":  "1.1.1.1",
				"port":     443,
				"protocol": "vless",
				"outbound": map[string]interface{}{
					"protocol": "vless",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(data)
	}))
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.AutoUpdate = false

	d := New(cfg)
	d.apiServer = NewAPIServer(sockPath, d.handleRequest)
	if err := d.apiServer.Start(d.ctx); err != nil {
		t.Fatalf("apiServer.Start failed: %v", err)
	}
	defer d.apiServer.Stop()

	// Client
	client := &Client{socketPath: sockPath}

	// 1. Ping
	if err := client.Ping(); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// 2. Add subscription
	resp, err := client.AddSubscription("testsub", ts.URL)
	if err != nil || !resp.Success {
		t.Fatalf("AddSubscription failed: %v, resp: %v", err, resp)
	}

	// 3. Update subscriptions
	resp, err = client.UpdateSubscriptions()
	if err != nil || !resp.Success {
		t.Fatalf("UpdateSubscriptions failed: %v, resp: %v", err, resp)
	}

	// 4. List configs
	configs, err := client.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs failed: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].Name != "Fast-Node" {
		t.Errorf("expected Fast-Node, got %s", configs[0].Name)
	}

	// 5. Check status
	status, err := client.Status()
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.Connected {
		t.Errorf("expected not connected")
	}

	// 6. Set mode
	resp, err = client.SetMode("proxy")
	if err != nil || !resp.Success {
		t.Fatalf("SetMode failed: %v", err)
	}

	status, err = client.Status()
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.Mode != config.ModeProxy {
		t.Errorf("expected ModeProxy, got %v", status.Mode)
	}
}
