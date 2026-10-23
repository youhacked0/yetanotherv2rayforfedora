// Package cli provides the Command Line Interface, interactive TUI, and subcommands.
package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/notxaked/customv2ray/internal/config"
	"github.com/notxaked/customv2ray/internal/daemon"
	"github.com/notxaked/customv2ray/internal/nm"
	"github.com/notxaked/customv2ray/internal/xray"
)

// Execute is the main entry point called from main().
func Execute() {
	args := os.Args[1:]

	if len(args) == 0 {
		if isatty.IsTerminal(os.Stdout.Fd()) {
			if err := StartTUI(); err != nil {
				fmt.Fprintf(os.Stderr, "Error starting TUI: %v\n", err)
				os.Exit(1)
			}
			return
		}
		// If invoked non-interactively (e.g. by NetworkManager as VPN plugin program)
		if !isatty.IsTerminal(os.Stdin.Fd()) {
			if err := nm.RunVPNService(); err != nil {
				fmt.Fprintf(os.Stderr, "NM VPN service error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		printHelp()
		return
	}

	cmd := args[0]
	cmdArgs := args[1:]

	if strings.HasPrefix(cmd, "--bus-name=") {
		targetBus := strings.TrimPrefix(cmd, "--bus-name=")
		if err := nm.RunVPNService(targetBus); err != nil {
			fmt.Fprintf(os.Stderr, "NM VPN service error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	switch cmd {
	case "tui":
		if err := StartTUI(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "shell", "interactive", "-i":
		runInteractiveShell()

	case "daemon":
		runDaemon()

	case "status":
		runStatus()

	case "connect", "up":
		runConnect(cmdArgs)

	case "disconnect", "down":
		runDisconnect()

	case "mode":
		runMode(cmdArgs)

	case "list", "servers":
		runList()

	case "config", "show-config":
		runConfig()

	case "sub", "subscription", "subscriptions":
		runSub(cmdArgs)

	case "install":
		runInstall()

	case "uninstall":
		runUninstall()

	case "sync":
		runSync()

	case "ping", "p":
		runPing(cmdArgs)

	case "log", "logs":
		runLogs(cmdArgs)

	case "--nm-vpn-service", "--bus-name":
		targetBus := ""
		if len(cmdArgs) > 0 {
			targetBus = cmdArgs[0]
		}
		if err := nm.RunVPNService(targetBus); err != nil {
			fmt.Fprintf(os.Stderr, "NM VPN service error: %v\n", err)
			os.Exit(1)
		}

	case "help", "--help", "-h":
		printHelp()

	case "version", "--version", "-v":
		fmt.Printf("%s v%s\n", config.AppName, config.Version)

	default:
		// If command matches a server name or ID, connect directly!
		client := daemon.NewClient()
		if resp, err := client.Connect(cmd); err == nil && resp.Success {
			fmt.Printf("Connected to %s\n", cmd)
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %q\nRun '%s help' for usage.\n", cmd, config.AppName)
		os.Exit(1)
	}
}

func runDaemon() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Daemon error: %v\n", err)
		os.Exit(1)
	}
}

func runStatus() {
	client := daemon.NewClient()
	status, err := client.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get status: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n=== CustomV2Ray Status ===\n")
	if status.Connected {
		fmt.Printf("State:       \033[32mCONNECTED\033[0m\n")
		if status.ActiveConfig != nil {
			fmt.Printf("Server:      %s (%s:%d)\n", status.ActiveConfig.Name, status.ActiveConfig.Address, status.ActiveConfig.Port)
			fmt.Printf("Protocol:    %s\n", status.ActiveConfig.FullProtocol())
		}
		fmt.Printf("Mode:        %s\n", status.Mode)

		// Test ping to gstatic generate_204
		lat, pErr := config.MeasureGstaticPing(2 * time.Second)
		if pErr != nil {
			fmt.Printf("Ping:        \033[31mn/a\033[0m\n")
		} else {
			fmt.Printf("Ping:        \033[32m%d ms\033[0m\n", lat.Milliseconds())
		}

		if status.Uptime != "" {
			fmt.Printf("Uptime:      %s\n", status.Uptime)
		}
		if status.ProxyAddr != "" {
			fmt.Printf("SOCKS5:      %s\n", status.ProxyAddr)
		}
		if status.HTTPAddr != "" {
			fmt.Printf("HTTP Proxy:  %s\n", status.HTTPAddr)
		}
	} else {
		fmt.Printf("State:       \033[31mDISCONNECTED\033[0m\n")
		fmt.Printf("Mode:        %s\n", status.Mode)
		fmt.Printf("Ping:        \033[31mn/a\033[0m\n")
	}
	fmt.Println()
}

func runConnect(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: %s connect <server-id-or-name>\n", config.AppName)
		os.Exit(1)
	}
	target := strings.Join(args, " ")
	client := daemon.NewClient()
	resp, err := client.Connect(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Connect failed: %v\n", err)
		os.Exit(1)
	}
	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Connect failed: %s\n", resp.Message)
		os.Exit(1)
	}
	fmt.Println(resp.Message)
}

func runDisconnect() {
	client := daemon.NewClient()
	resp, err := client.Disconnect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Disconnect failed: %v\n", err)
		os.Exit(1)
	}
	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Disconnect failed: %s\n", resp.Message)
		os.Exit(1)
	}
	fmt.Println(resp.Message)
}

func runMode(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: %s mode <proxy|tun|both>\n", config.AppName)
		os.Exit(1)
	}
	mode := strings.ToLower(args[0])
	if mode != "proxy" && mode != "tun" && mode != "both" {
		fmt.Fprintf(os.Stderr, "Invalid mode: %q (expected 'proxy', 'tun', or 'both')\n", mode)
		os.Exit(1)
	}

	client := daemon.NewClient()
	resp, err := client.SetMode(mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Set mode failed: %v\n", err)
		os.Exit(1)
	}
	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Set mode failed: %s\n", resp.Message)
		os.Exit(1)
	}
	fmt.Println(resp.Message)
}

func runList() {
	client := daemon.NewClient()
	configs, err := client.ListConfigs()
	if err != nil {
		// Fallback: read directly from saved config if daemon is not running
		cfg, cfgErr := config.LoadConfig()
		if cfgErr == nil && len(cfg.GetAllConfigs()) > 0 {
			configs = cfg.GetAllConfigs()
		} else {
			fmt.Fprintf(os.Stderr, "Failed to list servers: %v\n", err)
			os.Exit(1)
		}
	}

	if len(configs) == 0 {
		fmt.Println("No servers found. Add a subscription first:")
		fmt.Printf("  %s sub add <name> <https://...>\n", config.AppName)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPROTOCOL\tADDRESS\tPORT\tSUBSCRIPTION")
	fmt.Fprintln(w, "--\t----\t--------\t-------\t----\t------------")
	for _, c := range configs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", c.ID, c.Name, c.FullProtocol(), c.Address, c.Port, c.SubName)
	}
	w.Flush()
}

func runConfig() {
	client := daemon.NewClient()
	raw, err := client.ShowConfig()
	if err != nil || raw == "" {
		// Fallback: try reading current active config file directly
		for _, p := range []string{
			xray.CurrentConfigPath(),
			"/etc/customv2ray/xray/current.json",
		} {
			if data, rErr := os.ReadFile(p); rErr == nil && len(data) > 0 {
				fmt.Println(string(data))
				return
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("No active configuration")
		return
	}
	fmt.Println(raw)
}

func runPing(args []string) {
	fmt.Printf("Pinging %s...\n", config.PingURL)
	lat, err := config.MeasureGstaticPing(3 * time.Second)
	if err != nil {
		fmt.Printf("Ping: \033[31mn/a\033[0m (%v)\n", err)
		return
	}
	fmt.Printf("Ping: \033[32m%d ms\033[0m\n", lat.Milliseconds())
}

func runSub(args []string) {
	if len(args) == 0 {
		runSubList()
		return
	}

	subCmd := args[0]
	client := daemon.NewClient()

	switch subCmd {
	case "add":
		if len(args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s sub add <name> <url>\n", config.AppName)
			os.Exit(1)
		}
		name, url := args[1], args[2]
		resp, err := client.AddSubscription(name, url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Add subscription failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(resp.Message)
		fmt.Println("Updating subscriptions...")
		if uResp, err := client.UpdateSubscriptions(); err == nil {
			fmt.Println(uResp.Message)
		}

	case "remove", "rm", "delete":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: %s sub remove <name>\n", config.AppName)
			os.Exit(1)
		}
		name := args[1]
		resp, err := client.RemoveSubscription(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Remove subscription failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(resp.Message)

	case "update", "refresh":
		fmt.Println("Updating subscriptions...")
		resp, err := client.UpdateSubscriptions()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Update subscriptions failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(resp.Message)

	case "list", "ls":
		runSubList()

	default:
		fmt.Fprintf(os.Stderr, "Unknown sub command: %q\nUsage: %s sub [add|remove|update|list]\n", subCmd, config.AppName)
		os.Exit(1)
	}
}

func runSubList() {
	client := daemon.NewClient()
	subs, err := client.ListSubscriptions()
	if err != nil {
		// Fallback: read directly from saved config if daemon is not running
		cfg, cfgErr := config.LoadConfig()
		if cfgErr == nil {
			subs = cfg.Subscriptions
		} else {
			fmt.Fprintf(os.Stderr, "Failed to list subscriptions: %v\n", err)
			os.Exit(1)
		}
	}

	if len(subs) == 0 {
		fmt.Println("No subscriptions configured.")
		fmt.Printf("Add one with: %s sub add <name> <url>\n", config.AppName)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tSERVERS\tLAST UPDATED\tURL")
	fmt.Fprintln(w, "----\t-------\t------------\t---")
	for _, s := range subs {
		updated := "Never"
		if !s.UpdatedAt.IsZero() {
			updated = s.UpdatedAt.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", s.Name, len(s.Configs), updated, s.URL)
	}
	w.Flush()
}

func runInteractiveShell() {
	fmt.Printf("\033[1;35mCustomV2Ray Interactive Console v%s\033[0m\n", config.Version)
	fmt.Println("Type 'help' for commands, 'exit' or 'quit' to close.")
	fmt.Println()

	client := daemon.NewClient()
	scanner := bufio.NewScanner(os.Stdin)

	for {
		// Show prompt with current status
		statusText := "\033[31m[offline]\033[0m"
		if s, err := client.Status(); err == nil && s.Connected {
			statusText = fmt.Sprintf("\033[32m[%s | %s]\033[0m", s.ActiveConfig.Name, s.Mode)
		}

		fmt.Printf("%s v2ray> ", statusText)
		if !scanner.Scan() {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		cmd := parts[0]
		cmdArgs := parts[1:]

		switch cmd {
		case "exit", "quit", "q":
			fmt.Println("Bye!")
			return
		case "help", "?":
			printShellHelp()
		case "status":
			runStatus()
		case "list", "ls", "servers":
			runList()
		case "connect", "c":
			runConnect(cmdArgs)
		case "disconnect", "d":
			runDisconnect()
		case "mode", "m":
			runMode(cmdArgs)
		case "config":
			runConfig()
		case "sub":
			runSub(cmdArgs)
		case "sync":
			runSync()
		case "ping", "p":
			lat, pErr := config.MeasureGstaticPing(2 * time.Second)
			if pErr != nil {
				fmt.Printf("Ping: \033[31mn/a\033[0m\n")
			} else {
				fmt.Printf("Ping: \033[32m%d ms\033[0m\n", lat.Milliseconds())
			}
		case "log", "logs":
			runLogs(cmdArgs)
		case "tui":
			_ = StartTUI()
		case "clear", "cls":
			fmt.Print("\033[H\033[2J")
		default:
			// Try connecting
			if resp, err := client.Connect(cmd); err == nil && resp.Success {
				fmt.Println(resp.Message)
			} else {
				fmt.Printf("Unknown command: %s. Type 'help' for list of commands.\n", cmd)
			}
		}
	}
}

func printShellHelp() {
	fmt.Println(`Available commands:
  status               Show connection and daemon status
  list                 List all available server configurations
  connect <name|id>    Connect to specified server
  disconnect           Disconnect active connection
  mode <proxy|tun|both> Switch operating mode
  config               Show active xray core JSON configuration
  sub list             List all subscriptions
  sub add <name> <url> Add a subscription and fetch servers
  sub remove <name>    Remove a subscription
  sub update           Update subscriptions from remote servers
  sync                 Synchronize all server configs with NetworkManager VPN menu
  ping                 Test connection latency to gstatic generate_204 (returns ms or n/a)
  log [-n N]           Show daemon and Xray logs in real-time
  tui                  Launch full interactive TUI
  clear                Clear the terminal screen
  exit / quit          Exit the shell`)
}

func printHelp() {
	fmt.Printf(`CustomV2Ray v%s - Next-generation Xray VPN & Proxy Manager for Fedora Linux

Usage:
  %s [command] [arguments]

Commands:
  (no args)             Launch full interactive TUI (if in terminal)
  tui                   Launch interactive TUI
  shell, -i             Launch interactive console command prompt
  status                Show current VPN/proxy status and details
  connect <name|id>     Connect to a server configuration
  disconnect            Disconnect current connection
  mode <proxy|tun|both> Set operating mode:
                          tun   - Full system VPN routing via TUN/TPROXY
                          proxy - Local SOCKS5 (:10808) & HTTP (:10809)
                          both  - TUN system VPN + local SOCKS/HTTP proxies
  list                  List all available server configs
  config                Display active Xray core JSON configuration
  ping                  Test connection latency to gstatic generate_204
  sub add <name> <url>  Add new subscription URL (custom User-Agent: %s)
  sub remove <name>     Remove subscription
  sub update            Force update of all subscriptions (auto-updated hourly)
  sub list              List configured subscriptions
  sync                  Sync all server configs to NetworkManager (Fedora VPN menu)
  log, logs             Stream real-time daemon and Xray logs
  daemon                Run background daemon process
  install               Install systemd service, desktop entry, and NM VPN plugin
  uninstall             Uninstall systemd service, desktop entry, and NM plugin
  version               Show version information
`, config.Version, config.AppName, config.UserAgent)
}

func runSync() {
	client := daemon.NewClient()
	resp, err := client.Send(config.DaemonRequest{Command: "nm_sync"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Sync failed: %v\n", err)
		os.Exit(1)
	}
	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Sync failed: %s\n", resp.Message)
		os.Exit(1)
	}
	fmt.Println(resp.Message)
}

func runLogs(args []string) {
	lines := "50"
	follow := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-n" && i+1 < len(args) {
			lines = args[i+1]
			i++
		} else if strings.HasPrefix(a, "-n") {
			lines = strings.TrimPrefix(a, "-n")
		} else if a == "--no-follow" {
			follow = false
		}
	}

	cmdArgs := []string{"-u", "customv2ray.service", "-n", lines}
	if follow {
		cmdArgs = append(cmdArgs, "-f")
	}

	c := exec.Command("journalctl", cmdArgs...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin

	if err := c.Run(); err != nil {
		fmt.Println("\nTip: viewing systemd logs may require root:")
		fmt.Println("  sudo journalctl -u customv2ray -f")
	}
}

func runInstall() {
	if os.Getuid() != 0 {
		fmt.Fprintf(os.Stderr, "Error: 'install' requires root privileges. Run: sudo %s install\n", config.AppName)
		os.Exit(1)
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve executable path: %v\n", err)
		os.Exit(1)
	}
	self, _ = filepath.EvalSymlinks(self)

	targetBin := "/usr/local/bin/" + config.AppName
	v2raySymlink := "/usr/local/bin/v2ray"

	// 1. Copy or link binary safely
	if self != targetBin {
		// Stop service first if running so executable is not busy (ETXTBSY)
		_ = exec.Command("systemctl", "stop", "customv2ray").Run()

		data, err := os.ReadFile(self)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to read source binary %s: %v\n", self, err)
			os.Exit(1)
		}

		tmpBin := targetBin + ".tmp"
		if err := os.WriteFile(tmpBin, data, 0755); err != nil {
			// If cannot write temp file directly, try unlinking targetBin
			_ = os.Remove(targetBin)
			if err := os.WriteFile(targetBin, data, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to write %s: %v\n", targetBin, err)
				os.Exit(1)
			}
		} else {
			if err := os.Rename(tmpBin, targetBin); err != nil {
				_ = os.Remove(targetBin)
				if err := os.Rename(tmpBin, targetBin); err != nil {
					fmt.Fprintf(os.Stderr, "Error: failed to replace %s: %v\n", targetBin, err)
					os.Exit(1)
				}
			}
		}
		fmt.Printf("Successfully installed updated binary to %s\n", targetBin)
	}
	_ = os.Remove(v2raySymlink)
	if err := os.Symlink(targetBin, v2raySymlink); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to symlink %s: %v\n", v2raySymlink, err)
	} else {
		fmt.Printf("Created symlink %s -> %s\n", v2raySymlink, targetBin)
	}

	// 2. Install Desktop Entry
	desktopContent := fmt.Sprintf(`[Desktop Entry]
Name=CustomV2Ray
Comment=Xray VPN and Proxy Manager
Exec=%s tui
Icon=preferences-system-network
Terminal=true
Type=Application
Categories=Network;VPN;Settings;
Keywords=vpn;v2ray;xray;proxy;
`, targetBin)

	desktopPath := "/usr/share/applications/customv2ray.desktop"
	if err := os.WriteFile(desktopPath, []byte(desktopContent), 0644); err != nil {
		fmt.Printf("Warning: failed to write desktop entry: %v\n", err)
	} else {
		fmt.Printf("Installed desktop entry to %s\n", desktopPath)
	}

	// 3. Install systemd service
	serviceContent := fmt.Sprintf(`[Unit]
Description=CustomV2Ray VPN & Proxy Daemon
After=network.target NetworkManager.service
Wants=network.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, targetBin)

	servicePath := "/etc/systemd/system/customv2ray.service"
	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		fmt.Printf("Warning: failed to write systemd service: %v\n", err)
	} else {
		fmt.Printf("Installed systemd service to %s\n", servicePath)
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}

	// 4. Install NetworkManager VPN plugin
	cfg, _ := config.LoadConfig()
	nmMgr := nm.NewManager(cfg)
	if err := nmMgr.EnsureVPNPlugin(); err != nil {
		fmt.Printf("Warning: failed to register NM VPN plugin: %v\n", err)
	} else {
		fmt.Println("Registered NetworkManager VPN plugin and D-Bus policies")
		_ = exec.Command("systemctl", "reload", "dbus").Run()
		_ = exec.Command("systemctl", "restart", "NetworkManager").Run()

		allConfigs := cfg.GetAllConfigs()
		if len(allConfigs) > 0 {
			if err := nmMgr.SyncConfigs(allConfigs); err == nil {
				fmt.Printf("Synced %d server configs to NetworkManager VPN menu\n", len(allConfigs))
			}
		}
	}

	_ = exec.Command("systemctl", "enable", "customv2ray").Run()
	_ = exec.Command("systemctl", "restart", "customv2ray").Run()

	fmt.Println("\nInstallation complete!")
	fmt.Printf("Start daemon with:  sudo systemctl enable --now customv2ray\n")
	fmt.Printf("Launch CLI/TUI:     %s\n", config.AppName)
	fmt.Printf("Or simply:          v2ray\n")
}

func runUninstall() {
	if os.Getuid() != 0 {
		fmt.Fprintf(os.Stderr, "Error: 'uninstall' requires root privileges. Run: sudo %s uninstall\n", config.AppName)
		os.Exit(1)
	}

	_ = exec.Command("systemctl", "stop", "customv2ray").Run()
	_ = exec.Command("systemctl", "disable", "customv2ray").Run()
	_ = os.Remove("/etc/systemd/system/customv2ray.service")
	_ = exec.Command("systemctl", "daemon-reload").Run()

	_ = os.Remove("/usr/share/applications/customv2ray.desktop")
	_ = os.Remove("/usr/local/bin/v2ray")
	_ = os.Remove("/usr/local/bin/" + config.AppName)

	_ = os.Remove("/etc/NetworkManager/VPN/customv2ray.name")
	_ = os.Remove("/usr/share/dbus-1/system-services/org.freedesktop.NetworkManager.customv2ray.service")
	_ = os.Remove("/etc/dbus-1/system.d/customv2ray.conf")

	fmt.Println("CustomV2Ray has been uninstalled successfully.")
}

