package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppConfig_AddFindRemoveSubscription(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cv2ray-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := DefaultConfig()
	if cfg.UserAgent != "Xaked-Prilozhyha" {
		t.Errorf("expected UserAgent 'Xaked-Prilozhyha', got '%s'", cfg.UserAgent)
	}
	if cfg.UpdateInterval != 60 {
		t.Errorf("expected 60 min update interval, got %d", cfg.UpdateInterval)
	}

	// Add subscription
	err = cfg.AddSubscription("MySub", "https://example.com/sub")
	if err != nil {
		t.Fatalf("AddSubscription failed: %v", err)
	}

	sub := cfg.FindSubscription("MySub")
	if sub == nil {
		t.Fatalf("FindSubscription returned nil")
	}
	if sub.URL != "https://example.com/sub" {
		t.Errorf("expected URL 'https://example.com/sub', got '%s'", sub.URL)
	}

	// Add server config to subscription
	subIndex := -1
	for i := range cfg.Subscriptions {
		if cfg.Subscriptions[i].Name == "MySub" {
			subIndex = i
			break
		}
	}
	cfg.Subscriptions[subIndex].Configs = append(cfg.Subscriptions[subIndex].Configs, ServerConfig{
		ID:       "srv-1",
		Name:     "Server 1",
		Address:  "1.2.3.4",
		Port:     443,
		Protocol: "vless",
	})

	all := cfg.GetAllConfigs()
	if len(all) != 1 {
		t.Fatalf("expected 1 config, got %d", len(all))
	}

	found := cfg.FindConfig("srv-1")
	if found == nil || found.Name != "Server 1" {
		t.Errorf("FindConfig failed, got %v", found)
	}

	// Remove subscription
	err = cfg.RemoveSubscription("MySub")
	if err != nil {
		t.Fatalf("RemoveSubscription failed: %v", err)
	}

	if cfg.FindSubscription("MySub") != nil {
		t.Errorf("subscription should have been removed")
	}
}

func TestConfigDirAndDataDir(t *testing.T) {
	cd := ConfigDir()
	if cd == "" {
		t.Errorf("ConfigDir returned empty")
	}
	dd := DataDir()
	if dd == "" {
		t.Errorf("DataDir returned empty")
	}
	cf := ConfigFile()
	if filepath.Base(cf) != "config.json" {
		t.Errorf("expected config.json, got %s", cf)
	}
}

