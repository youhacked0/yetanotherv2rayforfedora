package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/notxaked/customv2ray/internal/config"
)

const (
	clientConnectTimeout = 5 * time.Second
	clientReadTimeout    = 30 * time.Second
)

// Client communicates with the running daemon over the Unix domain socket.
// It is used by the CLI to issue commands and receive responses.
type Client struct {
	socketPath string
}

// NewClient returns a Client that talks to the daemon at config.SocketPath.
func NewClient() *Client {
	return &Client{socketPath: config.SocketPath}
}

// Send sends a DaemonRequest and waits for a DaemonResponse.
//
// Each call opens a new connection, writes the JSON-encoded request, reads
// the JSON-encoded response, and closes the connection.
func (c *Client) Send(req config.DaemonRequest) (*config.DaemonResponse, error) {
	conn, err := net.DialTimeout("unix", c.socketPath, clientConnectTimeout)
	if err != nil {
		return nil, fmt.Errorf(
			"cannot connect to daemon: %w\n  (is the daemon running? start with: sudo %s daemon)",
			err, config.AppName,
		)
	}
	defer conn.Close()

	// Write request.
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	// Read response.
	if err := conn.SetReadDeadline(time.Now().Add(clientReadTimeout)); err != nil {
		return nil, fmt.Errorf("set read deadline: %w", err)
	}

	var resp config.DaemonResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return &resp, nil
}

// ---------- Convenience wrappers ----------

// Connect asks the daemon to connect to the server identified by configID.
func (c *Client) Connect(configID string) (*config.DaemonResponse, error) {
	return c.Send(config.DaemonRequest{
		Command: "connect",
		Args:    map[string]string{"config_id": configID},
	})
}

// Disconnect asks the daemon to tear down the active connection.
func (c *Client) Disconnect() (*config.DaemonResponse, error) {
	return c.Send(config.DaemonRequest{Command: "disconnect"})
}

// Status returns the current daemon status.
func (c *Client) Status() (*config.Status, error) {
	resp, err := c.Send(config.DaemonRequest{Command: "status"})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("status: %s", resp.Message)
	}

	var s config.Status
	if err := json.Unmarshal(resp.Data, &s); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	return &s, nil
}

// ListConfigs returns all server configs known to the daemon.
func (c *Client) ListConfigs() ([]config.ServerConfig, error) {
	resp, err := c.Send(config.DaemonRequest{Command: "list"})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("list: %s", resp.Message)
	}

	var cfgs []config.ServerConfig
	if err := json.Unmarshal(resp.Data, &cfgs); err != nil {
		return nil, fmt.Errorf("decode configs: %w", err)
	}
	return cfgs, nil
}

// AddSubscription adds a named subscription URL.
func (c *Client) AddSubscription(name, url string) (*config.DaemonResponse, error) {
	return c.Send(config.DaemonRequest{
		Command: "subscribe_add",
		Args:    map[string]string{"name": name, "url": url},
	})
}

// RemoveSubscription removes a subscription by name.
func (c *Client) RemoveSubscription(name string) (*config.DaemonResponse, error) {
	return c.Send(config.DaemonRequest{
		Command: "subscribe_remove",
		Args:    map[string]string{"name": name},
	})
}

// UpdateSubscriptions forces an update of all subscriptions.
func (c *Client) UpdateSubscriptions() (*config.DaemonResponse, error) {
	return c.Send(config.DaemonRequest{Command: "subscribe_update"})
}

// ListSubscriptions returns all configured subscriptions.
func (c *Client) ListSubscriptions() ([]config.Subscription, error) {
	resp, err := c.Send(config.DaemonRequest{Command: "subscribe_list"})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("subscribe_list: %s", resp.Message)
	}

	var subs []config.Subscription
	if err := json.Unmarshal(resp.Data, &subs); err != nil {
		return nil, fmt.Errorf("decode subscriptions: %w", err)
	}
	return subs, nil
}

// SetMode changes the operating mode (proxy, tun, both).
func (c *Client) SetMode(mode string) (*config.DaemonResponse, error) {
	return c.Send(config.DaemonRequest{
		Command: "mode",
		Args:    map[string]string{"mode": mode},
	})
}

// ShowConfig returns the current xray JSON configuration string.
func (c *Client) ShowConfig() (string, error) {
	resp, err := c.Send(config.DaemonRequest{Command: "config_show"})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("config_show: %s", resp.Message)
	}

	var raw string
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return "", fmt.Errorf("decode config: %w", err)
	}
	return raw, nil
}

// GetXrayConfig is an alias for ShowConfig.
func (c *Client) GetXrayConfig() (string, error) {
	return c.ShowConfig()
}

// Ping performs a health-check against the daemon.
func (c *Client) Ping() error {
	resp, err := c.Send(config.DaemonRequest{Command: "ping"})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("ping: %s", resp.Message)
	}
	return nil
}

// IsDaemonRunning returns true when the daemon responds to a ping.
func (c *Client) IsDaemonRunning() bool {
	return c.Ping() == nil
}
