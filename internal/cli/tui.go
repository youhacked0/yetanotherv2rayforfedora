package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/notxaked/customv2ray/internal/config"
	"github.com/notxaked/customv2ray/internal/daemon"
)

// Tab indices.
const (
	tabDashboard     = 0
	tabServers       = 1
	tabSubscriptions = 2
)

// Lipgloss styles.
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#af87ff")).
			PaddingLeft(1)

	activeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00ff87")).
			Bold(true)

	inactiveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ff5f87"))

	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffff87"))

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00d7ff"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#af87ff")).
			Padding(1, 2)

	tabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00d7ff")).
			Underline(true)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#626262"))

	cursorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00d7ff")).
			Bold(true)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00ff87"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262"))

	headerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#af87ff")).
			Bold(true).
			PaddingLeft(1)
)

// TUIModel is the main bubbletea model for the interactive TUI.
type TUIModel struct {
	client    *daemon.Client
	activeTab int
	tabs      []string

	// Dashboard
	status config.Status

	// Servers
	serverList   []config.ServerConfig
	serverCursor int

	// Subscriptions
	subList   []config.Subscription
	subCursor int

	// Input mode
	inputMode     bool
	inputField    string
	inputValue    string
	inputCallback func(string)

	// Multi-step input (for add subscription: name then URL)
	pendingSubName string

	// Dimensions
	width, height int

	// Status message
	statusMsg string
	statusErr bool

	// Help overlay
	showHelp bool

	// Ping display
	pingStr string

	// Xray config display
	showConfig    bool
	configContent string
	configScroll  int

	// Refresh ticker
	refreshing bool

	// Daemon available
	daemonUp bool

	err error
}

type configLoadedMsg struct {
	content string
	err     error
}

type pingMsg struct {
	latency time.Duration
	err     error
}

// NewTUIModel creates a new TUI model connected to the daemon.
func NewTUIModel() TUIModel {
	client := daemon.NewClient()

	return TUIModel{
		client:    client,
		activeTab: tabDashboard,
		tabs:      []string{"Dashboard", "Servers", "Subscriptions"},
		daemonUp:  true,
	}
}

// Init implements tea.Model. It fetches initial data and starts the tick.
func (m TUIModel) Init() tea.Cmd {
	return tea.Batch(
		fetchStatus(m.client),
		fetchConfigs(m.client),
		fetchSubs(m.client),
		tickCmd(),
		func() tea.Msg {
			lat, err := config.MeasureGstaticPing(2 * time.Second)
			return pingMsg{latency: lat, err: err}
		},
	)
}

// Update implements tea.Model.
func (m TUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case statusMsg:
		m.status = config.Status(msg)
		m.daemonUp = true
		m.err = nil
		return m, nil

	case configsMsg:
		m.serverList = []config.ServerConfig(msg)
		m.daemonUp = true
		return m, nil

	case subsMsg:
		m.subList = []config.Subscription(msg)
		m.daemonUp = true
		return m, nil

	case connectMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Connect failed: %v", msg.err)
			m.statusErr = true
		} else {
			m.statusMsg = "Connected"
			m.statusErr = false
		}
		m.pingStr = "pinging..."
		return m, tea.Batch(
			fetchStatus(m.client),
			fetchConfigs(m.client),
			func() tea.Msg {
				lat, err := config.MeasureGstaticPing(2 * time.Second)
				return pingMsg{latency: lat, err: err}
			},
		)

	case disconnectMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Disconnect failed: %v", msg.err)
			m.statusErr = true
		} else {
			m.statusMsg = "Disconnected"
			m.statusErr = false
		}
		m.pingStr = "n/a"
		return m, fetchStatus(m.client)

	case updateSubsMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Update failed: %v", msg.err)
			m.statusErr = true
		} else {
			m.statusMsg = "Subscriptions updated"
			m.statusErr = false
		}
		m.refreshing = false
		return m, tea.Batch(fetchSubs(m.client), fetchConfigs(m.client))

	case addSubMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Add failed: %v", msg.err)
			m.statusErr = true
		} else {
			m.statusMsg = "Subscription added"
			m.statusErr = false
		}
		return m, fetchSubs(m.client)

	case removeSubMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Remove failed: %v", msg.err)
			m.statusErr = true
		} else {
			m.statusMsg = "Subscription removed"
			m.statusErr = false
		}
		return m, tea.Batch(fetchSubs(m.client), fetchConfigs(m.client))

	case setModeMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Mode change failed: %v", msg.err)
			m.statusErr = true
		} else {
			m.statusMsg = "Mode changed"
			m.statusErr = false
		}
		return m, fetchStatus(m.client)

	case configLoadedMsg:
		if msg.err != nil {
			m.configContent = fmt.Sprintf("Error loading config: %v", msg.err)
		} else {
			m.configContent = msg.content
		}
		m.configScroll = 0
		return m, nil

	case pingMsg:
		if msg.err != nil {
			m.pingStr = "n/a"
		} else {
			m.pingStr = fmt.Sprintf("%d ms", msg.latency.Milliseconds())
		}
		return m, nil

	case errMsg:
		m.err = msg.err
		if msg.err != nil {
			// Check if daemon is unreachable.
			errStr := msg.err.Error()
			if strings.Contains(errStr, "connect") || strings.Contains(errStr, "dial") || strings.Contains(errStr, "refused") {
				m.daemonUp = false
			}
		}
		return m, nil

	case tickMsg:
		// Auto-refresh status every tick.
		return m, tea.Batch(fetchStatus(m.client), tickCmd())
	}

	return m, nil
}

// handleKey processes key events.
func (m TUIModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// If in input mode, handle input first.
	if m.inputMode {
		return m.handleInput(msg)
	}

	// If showing help, dismiss on any key.
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}

	key := msg.String()

	// If showing config, support scrolling or closing.
	if m.showConfig {
		switch key {
		case "esc", "q", "c", "enter":
			m.showConfig = false
			return m, nil
		case "up", "k":
			if m.configScroll > 0 {
				m.configScroll--
			}
			return m, nil
		case "down", "j":
			m.configScroll++
			return m, nil
		case "pageup":
			m.configScroll -= 10
			if m.configScroll < 0 {
				m.configScroll = 0
			}
			return m, nil
		case "pagedown":
			m.configScroll += 10
			return m, nil
		default:
			m.showConfig = false
			return m, nil
		}
	}

	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "?":
		m.showHelp = true
		return m, nil

	case "tab":
		m.activeTab = (m.activeTab + 1) % len(m.tabs)
		return m, nil

	case "shift+tab":
		m.activeTab = (m.activeTab - 1 + len(m.tabs)) % len(m.tabs)
		return m, nil

	case "d":
		m.statusMsg = "Disconnecting..."
		m.statusErr = false
		return m, disconnectCmd(m.client)

	case "m":
		// Cycle mode: proxy → tun → both → proxy.
		var nextMode string
		switch m.status.Mode {
		case config.ModeProxy:
			nextMode = string(config.ModeTUN)
		case config.ModeTUN:
			nextMode = string(config.ModeBoth)
		default:
			nextMode = string(config.ModeProxy)
		}
		return m, setModeCmd(m.client, nextMode)

	case "p":
		m.pingStr = "pinging..."
		return m, func() tea.Msg {
			lat, err := config.MeasureGstaticPing(2 * time.Second)
			return pingMsg{latency: lat, err: err}
		}

	case "c":
		m.showConfig = true
		m.configContent = "Loading Xray configuration..."
		m.configScroll = 0
		return m, func() tea.Msg {
			cfg, err := m.client.GetXrayConfig()
			if err != nil || cfg == "" {
				for _, p := range []string{"/etc/customv2ray/xray/current.json"} {
					if data, rErr := os.ReadFile(p); rErr == nil && len(data) > 0 {
						return configLoadedMsg{content: string(data)}
					}
				}
			}
			return configLoadedMsg{content: cfg, err: err}
		}
	}

	// Tab-specific key handling.
	switch m.activeTab {
	case tabDashboard, tabServers:
		return m.handleServerKeys(key)
	case tabSubscriptions:
		return m.handleSubKeys(key)
	}

	return m, nil
}

// handleServerKeys handles keys in the servers/dashboard views.
func (m TUIModel) handleServerKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.serverCursor > 0 {
			m.serverCursor--
		}
	case "down", "j":
		if m.serverCursor < len(m.serverList)-1 {
			m.serverCursor++
		}
	case "enter":
		if m.serverCursor >= 0 && m.serverCursor < len(m.serverList) {
			sc := m.serverList[m.serverCursor]
			m.statusMsg = fmt.Sprintf("Connecting to %s...", sc.Name)
			m.statusErr = false
			return m, connectCmd(m.client, sc.ID)
		}
	}
	return m, nil
}

// handleSubKeys handles keys in the subscriptions view.
func (m TUIModel) handleSubKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.subCursor > 0 {
			m.subCursor--
		}
	case "down", "j":
		if m.subCursor < len(m.subList)-1 {
			m.subCursor++
		}
	case "a":
		m.inputMode = true
		m.inputField = "Subscription name"
		m.inputValue = ""
		m.inputCallback = func(name string) {
			m.pendingSubName = name
			m.inputField = "Subscription URL"
			m.inputValue = ""
			m.inputMode = true
			m.inputCallback = nil // Will be set in the second round.
		}
		return m, nil
	case "r":
		if m.subCursor >= 0 && m.subCursor < len(m.subList) {
			sub := m.subList[m.subCursor]
			return m, removeSubCmd(m.client, sub.Name)
		}
	case "u":
		m.refreshing = true
		m.statusMsg = "Updating subscriptions..."
		m.statusErr = false
		return m, updateSubsCmd(m.client)
	}
	return m, nil
}

// handleInput handles key events when in input mode.
func (m TUIModel) handleInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "enter":
		value := m.inputValue
		m.inputMode = false

		// If we have a pending sub name, this is the URL step.
		if m.pendingSubName != "" {
			name := m.pendingSubName
			m.pendingSubName = ""
			m.inputValue = ""
			return m, addSubCmd(m.client, name, value)
		}

		// If callback is set, invoke it (name step).
		if m.inputCallback != nil {
			cb := m.inputCallback
			m.inputValue = ""
			cb(value)
			// After callback, we should be in second input mode.
			return m, nil
		}

		m.inputValue = ""
		return m, nil

	case "esc":
		m.inputMode = false
		m.inputValue = ""
		m.pendingSubName = ""
		m.inputCallback = nil
		return m, nil

	case "backspace":
		if len(m.inputValue) > 0 {
			m.inputValue = m.inputValue[:len(m.inputValue)-1]
		}

	default:
		// Only accept printable characters.
		if len(key) == 1 {
			m.inputValue += key
		} else if key == "space" {
			m.inputValue += " "
		}
	}
	return m, nil
}

// View implements tea.Model.
func (m TUIModel) View() string {
	if m.width == 0 {
		return "Initializing..."
	}

	// Help overlay.
	if m.showHelp {
		return m.viewHelp()
	}

	// Config overlay.
	if m.showConfig {
		return m.viewXrayConfig()
	}

	var b strings.Builder

	// Tab bar.
	b.WriteString(m.viewTabBar())
	b.WriteString("\n\n")

	// If daemon is not running, show warning.
	if !m.daemonUp {
		b.WriteString(m.viewDaemonDown())
		return b.String()
	}

	// Active tab content.
	switch m.activeTab {
	case tabDashboard:
		b.WriteString(m.viewDashboard())
	case tabServers:
		b.WriteString(m.viewServers())
	case tabSubscriptions:
		b.WriteString(m.viewSubscriptions())
	}

	// Status message.
	if m.statusMsg != "" {
		b.WriteString("\n")
		if m.statusErr {
			b.WriteString(" " + errorStyle.Render(m.statusMsg))
		} else {
			b.WriteString(" " + activeStyle.Render(m.statusMsg))
		}
	}

	// Input bar.
	if m.inputMode {
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf(" %s: %s█",
			infoStyle.Render(m.inputField),
			m.inputValue,
		))
	}

	// Footer.
	b.WriteString("\n\n")
	b.WriteString(m.viewFooter())

	return b.String()
}

// viewTabBar renders the tab bar.
func (m TUIModel) viewTabBar() string {
	var tabs []string
	for i, t := range m.tabs {
		if i == m.activeTab {
			tabs = append(tabs, tabActiveStyle.Render(t))
		} else {
			tabs = append(tabs, tabInactiveStyle.Render(t))
		}
	}
	return " " + strings.Join(tabs, dimStyle.Render("  │  "))
}

// viewDashboard renders the dashboard view.
func (m TUIModel) viewDashboard() string {
	var b strings.Builder

	// Status box.
	var statusLine string
	if m.status.Connected {
		statusLine = activeStyle.Render("● Connected")
	} else {
		statusLine = errorStyle.Render("○ Disconnected")
	}
	statusLine += dimStyle.Render(" │ ")
	statusLine += infoStyle.Render(modeLabel(m.status.Mode))

	if m.status.Uptime != "" {
		statusLine += dimStyle.Render(" │ ")
		statusLine += fmt.Sprintf("Uptime: %s", m.status.Uptime)
	}

	var details strings.Builder
	details.WriteString(statusLine)

	if m.status.ActiveConfig != nil {
		details.WriteString("\n")
		details.WriteString(fmt.Sprintf("Server: %s (%s)",
			activeStyle.Render(m.status.ActiveConfig.Name),
			m.status.ActiveConfig.FullProtocol()))
		details.WriteString(dimStyle.Render(" │ "))
		details.WriteString(fmt.Sprintf("%s:%d",
			m.status.ActiveConfig.Address,
			m.status.ActiveConfig.Port))
	}

	details.WriteString("\n")
	pingDisplay := m.pingStr
	if pingDisplay == "" {
		pingDisplay = "..."
	}
	if pingDisplay == "n/a" {
		details.WriteString(fmt.Sprintf("Ping:   %s", errorStyle.Render("n/a")))
	} else if strings.HasSuffix(pingDisplay, "ms") {
		details.WriteString(fmt.Sprintf("Ping:   %s", activeStyle.Render(pingDisplay)))
	} else {
		details.WriteString(fmt.Sprintf("Ping:   %s", dimStyle.Render(pingDisplay)))
	}

	if m.status.ProxyAddr != "" || m.status.HTTPAddr != "" {
		details.WriteString("\n")
		parts := []string{}
		if m.status.ProxyAddr != "" {
			parts = append(parts, fmt.Sprintf("SOCKS5: %s", m.status.ProxyAddr))
		}
		if m.status.HTTPAddr != "" {
			parts = append(parts, fmt.Sprintf("HTTP: %s", m.status.HTTPAddr))
		}
		details.WriteString(strings.Join(parts, dimStyle.Render(" │ ")))
	}

	boxWidth := m.width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}
	if boxWidth > 70 {
		boxWidth = 70
	}

	box := boxStyle.Width(boxWidth).Render(details.String())
	header := titleStyle.Render(fmt.Sprintf("CustomV2Ray v%s", config.Version))
	b.WriteString(header + "\n")
	b.WriteString(box)

	// Server list (compact).
	b.WriteString("\n\n")
	b.WriteString(headerStyle.Render("─── Servers ") + dimStyle.Render(strings.Repeat("─", maxInt(boxWidth-12, 0))))
	b.WriteString("\n")

	maxVisible := m.maxListHeight()
	start, end := visibleRange(m.serverCursor, len(m.serverList), maxVisible)

	for i := start; i < end; i++ {
		sc := m.serverList[i]
		prefix := "  "
		isActive := m.status.ActiveConfig != nil && m.status.ActiveConfig.ID == sc.ID
		if isActive {
			prefix = activeStyle.Render("* ")
		}

		line := fmt.Sprintf("%-18s%s%-19s%s%s:%d",
			sc.Name,
			dimStyle.Render(" │ "),
			sc.FullProtocol(),
			dimStyle.Render(" │ "),
			sc.Address, sc.Port)

		if i == m.serverCursor {
			b.WriteString(cursorStyle.Render(" > ") + prefix + selectedStyle.Render(line) + "\n")
		} else {
			b.WriteString("   " + prefix + line + "\n")
		}
	}

	if len(m.serverList) == 0 {
		b.WriteString(dimStyle.Render("   No servers available. Add a subscription first.") + "\n")
	}

	return b.String()
}

// viewServers renders the servers view.
func (m TUIModel) viewServers() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("─── Available Servers ") + dimStyle.Render(strings.Repeat("─", maxInt(m.contentWidth()-22, 0))))
	b.WriteString("\n\n")

	maxVisible := m.maxListHeight()
	start, end := visibleRange(m.serverCursor, len(m.serverList), maxVisible)

	for i := start; i < end; i++ {
		sc := m.serverList[i]
		prefix := "  "
		isActive := m.status.ActiveConfig != nil && m.status.ActiveConfig.ID == sc.ID
		if isActive {
			prefix = activeStyle.Render("* ")
		}

		line := fmt.Sprintf("%-18s%s%-19s%s%-22s%s%s",
			sc.Name,
			dimStyle.Render(" │ "),
			sc.FullProtocol(),
			dimStyle.Render(" │ "),
			fmt.Sprintf("%s:%d", sc.Address, sc.Port),
			dimStyle.Render(" │ "),
			sc.SubName)

		if i == m.serverCursor {
			b.WriteString(cursorStyle.Render(" > ") + prefix + selectedStyle.Render(line) + "\n")
		} else {
			b.WriteString("   " + prefix + line + "\n")
		}
	}

	if len(m.serverList) == 0 {
		b.WriteString(dimStyle.Render("   No servers available. Add a subscription first.") + "\n")
	}

	return b.String()
}

// viewSubscriptions renders the subscriptions view.
func (m TUIModel) viewSubscriptions() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("─── Subscriptions ") + dimStyle.Render(strings.Repeat("─", maxInt(m.contentWidth()-18, 0))))
	b.WriteString("\n\n")

	maxVisible := m.maxListHeight()
	start, end := visibleRange(m.subCursor, len(m.subList), maxVisible)

	for i := start; i < end; i++ {
		sub := m.subList[i]

		ago := timeAgo(sub.UpdatedAt)
		line := fmt.Sprintf("%-18s%s%d configs%s%s",
			sub.Name,
			dimStyle.Render(" │ "),
			len(sub.Configs),
			dimStyle.Render(" │ "),
			fmt.Sprintf("Updated: %s", ago))

		if !sub.Enabled {
			line += warnStyle.Render(" (disabled)")
		}

		if i == m.subCursor {
			b.WriteString(cursorStyle.Render(" > ") + selectedStyle.Render(line) + "\n")
		} else {
			b.WriteString("   " + line + "\n")
		}
	}

	if len(m.subList) == 0 {
		b.WriteString(dimStyle.Render("   No subscriptions. Press 'a' to add one.") + "\n")
	}

	return b.String()
}

// viewFooter renders context-sensitive footer help.
func (m TUIModel) viewFooter() string {
	var parts []string

	parts = append(parts, dimStyle.Render("Tab")+": switch view")

	switch m.activeTab {
	case tabDashboard, tabServers:
		parts = append(parts, dimStyle.Render("Enter")+": connect")
		parts = append(parts, dimStyle.Render("d")+": disconnect")
	case tabSubscriptions:
		parts = append(parts, dimStyle.Render("a")+": add")
		parts = append(parts, dimStyle.Render("r")+": remove")
		parts = append(parts, dimStyle.Render("u")+": update")
	}

	parts = append(parts, dimStyle.Render("p")+": ping")
	parts = append(parts, dimStyle.Render("c")+": config")
	parts = append(parts, dimStyle.Render("m")+": mode")
	parts = append(parts, dimStyle.Render("?")+": help")
	parts = append(parts, dimStyle.Render("q")+": quit")

	return " " + strings.Join(parts, dimStyle.Render(" │ "))
}

// viewHelp renders the help overlay.
func (m TUIModel) viewHelp() string {
	help := `
  Key Bindings
  ────────────────────────────────────────

  Tab / Shift+Tab    Switch tabs
  ↑/↓ or j/k         Navigate lists
  Enter               Connect to selected server
  d                   Disconnect
  m                   Cycle mode (proxy → tun → both)
  p                   Test ping (gstatic generate_204)
  c                   Show xray config (JSON)
  a                   Add subscription
  r                   Remove selected subscription
  u                   Update all subscriptions
  ?                   Toggle this help
  q / Ctrl+C          Quit

  Press any key to close...
`
	boxWidth := m.width - 4
	if boxWidth > 60 {
		boxWidth = 60
	}
	return boxStyle.Width(boxWidth).Render(
		titleStyle.Render("Help") + "\n" + help,
	)
}

// viewXrayConfig renders the xray config overlay with scrolling support.
func (m TUIModel) viewXrayConfig() string {
	boxWidth := m.width - 4
	if boxWidth > 100 {
		boxWidth = 100
	}
	if boxWidth < 40 {
		boxWidth = 40
	}

	content := m.configContent
	if content == "" {
		content = "No active configuration."
	}

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	maxLines := m.height - 8
	if maxLines < 6 {
		maxLines = 15
	}

	start := m.configScroll
	if start > totalLines-maxLines {
		start = totalLines - maxLines
	}
	if start < 0 {
		start = 0
	}

	end := start + maxLines
	if end > totalLines {
		end = totalLines
	}

	visibleLines := lines[start:end]
	scrollInfo := fmt.Sprintf("Lines %d-%d of %d (Use ↑/↓ or j/k to scroll, Esc/q/c to close)", start+1, end, totalLines)

	return boxStyle.Width(boxWidth).Render(
		titleStyle.Render("Xray Core Configuration (JSON)") + "\n\n" +
			strings.Join(visibleLines, "\n") + "\n\n" +
			dimStyle.Render(scrollInfo),
	)
}

// viewDaemonDown renders a warning when the daemon is not reachable.
func (m TUIModel) viewDaemonDown() string {
	msg := errorStyle.Render("⚠ Daemon not running") + "\n\n"
	msg += " The CustomV2Ray daemon is not reachable.\n"
	msg += " Start it with:\n\n"
	msg += infoStyle.Render("   sudo systemctl start customv2ray") + "\n\n"
	msg += " Or run in foreground:\n\n"
	msg += infoStyle.Render("   sudo customv2ray daemon") + "\n\n"
	msg += dimStyle.Render(" Press q to quit.")
	return boxStyle.Render(msg)
}

// Helper functions.

func modeLabel(mode config.Mode) string {
	switch mode {
	case config.ModeProxy:
		return "Proxy Mode"
	case config.ModeTUN:
		return "TUN Mode"
	case config.ModeBoth:
		return "TUN + Proxy"
	default:
		return string(mode)
	}
}

func timeAgo(t interface{ IsZero() bool }) string {
	// Accept time.Time via the IsZero interface check.
	return timeAgoImpl(t)
}

func timeAgoImpl(t interface{}) string {
	// We need concrete time.Time, import is in tui_commands.go via tickMsg.
	// Using fmt.Sprintf with %v is fine for now; real implementation uses time pkg.
	return fmt.Sprintf("%v ago", t)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m TUIModel) contentWidth() int {
	w := m.width - 4
	if w < 40 {
		w = 40
	}
	if w > 80 {
		w = 80
	}
	return w
}

func (m TUIModel) maxListHeight() int {
	// Reserve space for header, tabs, footer, status bar, etc.
	available := m.height - 14
	if available < 5 {
		available = 5
	}
	return available
}

// visibleRange computes the start/end indices for a scrollable list.
func visibleRange(cursor, total, maxVisible int) (int, int) {
	if total <= maxVisible {
		return 0, total
	}

	start := cursor - maxVisible/2
	if start < 0 {
		start = 0
	}

	end := start + maxVisible
	if end > total {
		end = total
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	return start, end
}

// StartTUI launches the interactive TUI.
func StartTUI() error {
	m := NewTUIModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
