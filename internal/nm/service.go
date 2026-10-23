// Package nm implements NetworkManager VPN plugin integration.
package nm

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/notxaked/customv2ray/internal/config"
)

const (
	vpnInterface = "org.freedesktop.NetworkManager.VPN.Plugin"
	vpnPath      = "/org/freedesktop/NetworkManager/VPN/Plugin"

	// NM VPN states
	vpnStateUnknown  = uint32(0)
	vpnStateInit     = uint32(1)
	vpnStateShutdown = uint32(2)
	vpnStateStarting = uint32(3)
	vpnStateStarted  = uint32(4)
	vpnStateStopping = uint32(5)
	vpnStateStopped  = uint32(6)
)

// VPNPlugin implements the org.freedesktop.NetworkManager.VPN.Plugin D-Bus interface.
type VPNPlugin struct {
	conn         *dbus.Conn
	connectFn    func(name string) error
	disconnectFn func() error
	mu           sync.Mutex
}

func sendToDaemon(cmd string, args map[string]string) (*config.DaemonResponse, error) {
	conn, err := net.DialTimeout("unix", config.SocketPath, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := config.DaemonRequest{Command: cmd, Args: args}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var resp config.DaemonResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Connect is called by NetworkManager to initiate a VPN connection.
func (p *VPNPlugin) Connect(connection map[string]map[string]dbus.Variant) *dbus.Error {
	log.Println("NM VPN Plugin: Connect requested")

	connSettings, ok := connection["connection"]
	if !ok {
		p.emitState(vpnStateStopped)
		return dbus.MakeFailedError(fmt.Errorf("missing connection settings"))
	}

	idVariant, ok := connSettings["id"]
	if !ok {
		p.emitState(vpnStateStopped)
		return dbus.MakeFailedError(fmt.Errorf("missing connection id"))
	}

	connName, ok := idVariant.Value().(string)
	if !ok {
		p.emitState(vpnStateStopped)
		return dbus.MakeFailedError(fmt.Errorf("invalid connection id type"))
	}

	// Connection name is "CustomV2Ray - <ServerName>"
	serverName := strings.TrimPrefix(connName, defaultConnPrefix+" - ")
	serverName = strings.TrimSpace(serverName)
	log.Printf("NM VPN Plugin: connecting to server %q (from connection %q)", serverName, connName)

	p.emitState(vpnStateStarting)

	go func() {
		var err error
		if p.connectFn != nil {
			err = p.connectFn(serverName)
		} else {
			resp, rErr := sendToDaemon("connect_nm", map[string]string{"config_id": serverName})
			if rErr != nil {
				err = rErr
			} else if resp != nil && !resp.Success {
				err = fmt.Errorf("%s", resp.Message)
			}
		}

		if err != nil {
			errStr := err.Error()
			log.Printf("NM VPN Plugin: error connecting: %s", errStr)
			p.emitFailure(errStr)
			p.emitState(vpnStateStopped)
			return
		}

		p.emitConfig()
		p.emitIp4Config()
		p.emitState(vpnStateStarted)
		log.Println("NM VPN Plugin: connected successfully, state=STARTED")
	}()

	return nil
}

// ConnectInteractive is called by NetworkManager for interactive VPN activation.
func (p *VPNPlugin) ConnectInteractive(connection map[string]map[string]dbus.Variant, details map[string]dbus.Variant) *dbus.Error {
	return p.Connect(connection)
}

// Disconnect is called by NetworkManager to tear down the VPN connection.
func (p *VPNPlugin) Disconnect() *dbus.Error {
	log.Println("NM VPN Plugin: Disconnect requested")
	p.emitState(vpnStateStopping)

	if p.disconnectFn != nil {
		_ = p.disconnectFn()
	} else {
		_, err := sendToDaemon("disconnect_nm", nil)
		if err != nil {
			log.Printf("NM VPN Plugin: disconnect error: %v", err)
		}
	}

	p.emitState(vpnStateStopped)
	log.Println("NM VPN Plugin: disconnected, state=STOPPED")
	return nil
}

// NeedSecrets returns the setting name that requires secrets (none for CustomV2Ray).
func (p *VPNPlugin) NeedSecrets(connection map[string]map[string]dbus.Variant) (string, *dbus.Error) {
	return "", nil
}

// SetConfig receives configuration dictionary (no-op stub).
func (p *VPNPlugin) SetConfig(config map[string]dbus.Variant) *dbus.Error {
	return nil
}

// SetIp4Config receives IPv4 config (no-op stub).
func (p *VPNPlugin) SetIp4Config(config map[string]dbus.Variant) *dbus.Error {
	return nil
}

// SetIp6Config receives IPv6 config (no-op stub).
func (p *VPNPlugin) SetIp6Config(config map[string]dbus.Variant) *dbus.Error {
	return nil
}

// SetFailure reports a failure.
func (p *VPNPlugin) SetFailure(reason string) *dbus.Error {
	p.emitFailure(reason)
	p.emitState(vpnStateStopped)
	return nil
}

// EmitStarted signals NetworkManager that the connection is active.
func (p *VPNPlugin) EmitStarted() {
	p.emitConfig()
	p.emitIp4Config()
	p.emitState(vpnStateStarted)
}

// EmitStopped signals NetworkManager that the connection is down.
func (p *VPNPlugin) EmitStopped() {
	p.emitState(vpnStateStopped)
}

func (p *VPNPlugin) emitConfig() {
	if p.conn == nil {
		return
	}
	gwBytes := net.ParseIP("198.18.0.1").To4()
	gwUint := binary.LittleEndian.Uint32(gwBytes)

	cfg := map[string]dbus.Variant{
		"tundev":      dbus.MakeVariant("cv2ray0"),
		"can-persist": dbus.MakeVariant(true),
		"has-ip4":     dbus.MakeVariant(true),
		"gateway":     dbus.MakeVariant(gwUint),
		"mtu":         dbus.MakeVariant(uint32(1500)),
	}
	_ = p.conn.Emit(vpnPath, vpnInterface+".Config", cfg)
}

func (p *VPNPlugin) emitIp4Config() {
	if p.conn == nil {
		return
	}
	ipBytes := net.ParseIP("198.18.0.1").To4()
	ipUint := binary.LittleEndian.Uint32(ipBytes)

	ip4Config := map[string]dbus.Variant{
		"address":          dbus.MakeVariant(ipUint),
		"prefix":           dbus.MakeVariant(uint32(32)),
		"gateway":          dbus.MakeVariant(ipUint),
		"internal-gateway": dbus.MakeVariant(ipUint),
		"never-default":    dbus.MakeVariant(true),
	}
	_ = p.conn.Emit(vpnPath, vpnInterface+".Ip4Config", ip4Config)
}

func (p *VPNPlugin) emitState(state uint32) {
	if p.conn == nil {
		return
	}
	_ = p.conn.Emit(vpnPath, vpnInterface+".StateChanged", state)
}

func (p *VPNPlugin) emitFailure(reason string) {
	if p.conn == nil {
		return
	}
	_ = p.conn.Emit(vpnPath, vpnInterface+".Failure", uint32(1))
}

// StartDBusService registers the NetworkManager VPN Plugin on the system bus.
// It runs in the background within the daemon process and uses the provided callbacks.
func StartDBusService(ctx context.Context, connectFn func(string) error, disconnectFn func() error) (*VPNPlugin, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect to D-Bus system bus: %w", err)
	}

	reply, err := conn.RequestName(vpnServiceName, dbus.NameFlagDoNotQueue)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("request D-Bus name %q: %w", vpnServiceName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		_ = conn.Close()
		return nil, fmt.Errorf("name %q already taken on system bus (reply code %d)", vpnServiceName, reply)
	}

	plugin := &VPNPlugin{
		conn:         conn,
		connectFn:    connectFn,
		disconnectFn: disconnectFn,
	}

	if err := conn.Export(plugin, vpnPath, vpnInterface); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("export VPNPlugin D-Bus object: %w", err)
	}

	exportIntrospectable(conn)

	log.Printf("CustomV2Ray NM VPN service registered on %s (%s)", vpnServiceName, vpnPath)
	plugin.emitState(vpnStateInit)

	go func() {
		<-ctx.Done()
		log.Println("CustomV2Ray NM VPN service stopping...")
		plugin.emitState(vpnStateShutdown)
		_ = conn.Close()
	}()

	return plugin, nil
}

// RunVPNService starts the NetworkManager D-Bus VPN service as a standalone process.
func RunVPNService(busNames ...string) error {
	serviceName := vpnServiceName
	if len(busNames) > 0 && busNames[0] != "" {
		serviceName = busNames[0]
	}
	log.Printf("Starting CustomV2Ray NetworkManager VPN service (bus name: %s)...", serviceName)

	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("connect to D-Bus system bus: %w", err)
	}
	defer conn.Close()

	reply, err := conn.RequestName(serviceName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("request D-Bus name %q: %w", serviceName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("name %q already taken on system bus", serviceName)
	}

	plugin := &VPNPlugin{
		conn: conn,
	}

	if err := conn.Export(plugin, vpnPath, vpnInterface); err != nil {
		return fmt.Errorf("export VPNPlugin D-Bus object: %w", err)
	}

	exportIntrospectable(conn)

	log.Printf("CustomV2Ray NM VPN service registered on %s (%s)", serviceName, vpnPath)
	plugin.emitState(vpnStateInit)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("CustomV2Ray NM VPN service stopping...")
	plugin.emitState(vpnStateShutdown)
	return nil
}

func exportIntrospectable(conn *dbus.Conn) {
	node := &introspect.Node{
		Name: vpnPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{
				Name: vpnInterface,
				Methods: []introspect.Method{
					{
						Name: "Connect",
						Args: []introspect.Arg{
							{Name: "connection", Type: "a{sa{sv}}", Direction: "in"},
						},
					},
					{
						Name: "ConnectInteractive",
						Args: []introspect.Arg{
							{Name: "connection", Type: "a{sa{sv}}", Direction: "in"},
							{Name: "details", Type: "a{sv}", Direction: "in"},
						},
					},
					{
						Name: "Disconnect",
					},
					{
						Name: "NeedSecrets",
						Args: []introspect.Arg{
							{Name: "connection", Type: "a{sa{sv}}", Direction: "in"},
							{Name: "setting_name", Type: "s", Direction: "out"},
						},
					},
					{
						Name: "SetConfig",
						Args: []introspect.Arg{
							{Name: "config", Type: "a{sv}", Direction: "in"},
						},
					},
					{
						Name: "SetIp4Config",
						Args: []introspect.Arg{
							{Name: "config", Type: "a{sv}", Direction: "in"},
						},
					},
					{
						Name: "SetIp6Config",
						Args: []introspect.Arg{
							{Name: "config", Type: "a{sv}", Direction: "in"},
						},
					},
					{
						Name: "SetFailure",
						Args: []introspect.Arg{
							{Name: "reason", Type: "s", Direction: "in"},
						},
					},
				},
				Signals: []introspect.Signal{
					{
						Name: "StateChanged",
						Args: []introspect.Arg{{Name: "state", Type: "u"}},
					},
					{
						Name: "Config",
						Args: []introspect.Arg{{Name: "config", Type: "a{sv}"}},
					},
					{
						Name: "Ip4Config",
						Args: []introspect.Arg{{Name: "ip4config", Type: "a{sv}"}},
					},
					{
						Name: "Failure",
						Args: []introspect.Arg{{Name: "reason", Type: "u"}},
					},
				},
			},
		},
	}
	_ = conn.Export(introspect.NewIntrospectable(node), vpnPath, "org.freedesktop.DBus.Introspectable")
}
