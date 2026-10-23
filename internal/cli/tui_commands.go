package cli

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/notxaked/customv2ray/internal/config"
	"github.com/notxaked/customv2ray/internal/daemon"
)

// Message types returned by tea.Cmd functions.
type statusMsg config.Status
type configsMsg []config.ServerConfig
type subsMsg []config.Subscription
type connectMsg struct{ err error }
type disconnectMsg struct{ err error }
type updateSubsMsg struct{ err error }
type addSubMsg struct{ err error }
type removeSubMsg struct{ err error }
type setModeMsg struct{ err error }
type errMsg struct{ err error }
type tickMsg time.Time

// fetchStatus returns a tea.Cmd that fetches the current daemon status.
func fetchStatus(client *daemon.Client) tea.Cmd {
	return func() tea.Msg {
		status, err := client.Status()
		if err != nil {
			return errMsg{err: err}
		}
		return statusMsg(*status)
	}
}

// fetchConfigs returns a tea.Cmd that fetches the list of available server configs.
func fetchConfigs(client *daemon.Client) tea.Cmd {
	return func() tea.Msg {
		configs, err := client.ListConfigs()
		if err != nil {
			return errMsg{err: err}
		}
		return configsMsg(configs)
	}
}

// fetchSubs returns a tea.Cmd that fetches the list of subscriptions.
func fetchSubs(client *daemon.Client) tea.Cmd {
	return func() tea.Msg {
		subs, err := client.ListSubscriptions()
		if err != nil {
			return errMsg{err: err}
		}
		return subsMsg(subs)
	}
}

// connectCmd returns a tea.Cmd that connects to the specified config.
func connectCmd(client *daemon.Client, configID string) tea.Cmd {
	return func() tea.Msg {
		_, err := client.Connect(configID)
		return connectMsg{err: err}
	}
}

// disconnectCmd returns a tea.Cmd that disconnects the active connection.
func disconnectCmd(client *daemon.Client) tea.Cmd {
	return func() tea.Msg {
		_, err := client.Disconnect()
		return disconnectMsg{err: err}
	}
}

// updateSubsCmd returns a tea.Cmd that triggers an update of all subscriptions.
func updateSubsCmd(client *daemon.Client) tea.Cmd {
	return func() tea.Msg {
		_, err := client.UpdateSubscriptions()
		return updateSubsMsg{err: err}
	}
}

// addSubCmd returns a tea.Cmd that adds a new subscription.
func addSubCmd(client *daemon.Client, name, url string) tea.Cmd {
	return func() tea.Msg {
		_, err := client.AddSubscription(name, url)
		return addSubMsg{err: err}
	}
}

// removeSubCmd returns a tea.Cmd that removes a subscription by name.
func removeSubCmd(client *daemon.Client, name string) tea.Cmd {
	return func() tea.Msg {
		_, err := client.RemoveSubscription(name)
		return removeSubMsg{err: err}
	}
}

// setModeCmd returns a tea.Cmd that sets the operating mode.
func setModeCmd(client *daemon.Client, mode string) tea.Cmd {
	return func() tea.Msg {
		_, err := client.SetMode(mode)
		return setModeMsg{err: err}
	}
}

// tickCmd returns a tea.Cmd that fires a tickMsg after 2 seconds.
func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
