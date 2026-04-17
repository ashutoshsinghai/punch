package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Message represents a single chat entry.
type Message struct {
	From string
	Body string
	At   time.Time
}

// SendFn is called by the UI when the user submits a message.
type SendFn func(msg string) error

// model is the Bubbletea model for the chat TUI.
type model struct {
	messages       []Message
	input          string
	myName         string
	peerName       string
	send           SendFn
	err            error
	width          int
	height         int
	progress       string // live transfer progress line; empty = hidden
	pendingConfirm bool   // waiting for y/N response to an incoming file
	confirmText    string // the confirmation question to display
}

// IncomingMsg is a Bubbletea message for a message received from the peer.
type IncomingMsg struct {
	From string
	Body string
}

// SystemMsg is a Bubbletea message for a system/status notification.
type SystemMsg struct {
	Text string
}

// ProgressMsg updates the live transfer progress bar.
// Empty Text clears it.
type ProgressMsg struct {
	Text string
}

// ConfirmMsg puts the UI into confirmation mode for an incoming file transfer.
type ConfirmMsg struct {
	Text string // e.g. "alice wants to send report.pdf (2.3 MB)"
}

// ErrMsg signals a fatal error.
type ErrMsg struct{ Err error }

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")).
			Padding(0, 1)

	myStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true)

	peerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)

	timeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	sysStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	confirmStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220")).
			Bold(true)

	inputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	errStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)
)

// NewChat creates and starts the Bubbletea chat program.
func NewChat(myName, peerName string, send SendFn) *tea.Program {
	m := model{
		myName:   myName,
		peerName: peerName,
		send:     send,
	}
	return tea.NewProgram(m, tea.WithAltScreen())
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyEnter:
			text := strings.TrimSpace(m.input)
			m.input = ""

			if m.pendingConfirm {
				// Any input: y/yes = accept, anything else = decline.
				var reply string
				if text == "y" || text == "yes" {
					reply = "__CONFIRM__:yes"
				} else {
					reply = "__CONFIRM__:no"
				}
				m.pendingConfirm = false
				m.confirmText = ""
				sendFn := m.send
				return m, func() tea.Msg {
					sendFn(reply) //nolint:errcheck
					return nil
				}
			}

			if text == "" {
				return m, nil
			}
			if text == "/quit" || text == "/exit" {
				return m, tea.Quit
			}

			// Commands handled by cmd layer — don't echo locally.
			isCmd := text == "/ping" || text == "/ip" || strings.HasPrefix(text, "/send ")
			if !isCmd {
				m.messages = append(m.messages, Message{
					From: m.myName,
					Body: text,
					At:   time.Now(),
				})
			}
			sendText := text
			sendFn := m.send
			return m, func() tea.Msg {
				if err := sendFn(sendText); err != nil {
					return ErrMsg{Err: err}
				}
				return nil
			}

		case tea.KeySpace:
			m.input += " "

		case tea.KeyBackspace, tea.KeyDelete:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}

		default:
			if msg.Type == tea.KeyRunes {
				m.input += string(msg.Runes)
			}
		}

	case IncomingMsg:
		m.messages = append(m.messages, Message{
			From: msg.From,
			Body: msg.Body,
			At:   time.Now(),
		})

	case SystemMsg:
		m.messages = append(m.messages, Message{
			From: "",
			Body: msg.Text,
			At:   time.Now(),
		})

	case ProgressMsg:
		m.progress = msg.Text

	case ConfirmMsg:
		m.pendingConfirm = true
		m.confirmText = msg.Text

	case ErrMsg:
		m.err = msg.Err
	}

	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return ""
	}

	headerLine := headerStyle.Render(
		fmt.Sprintf("punch — %s ↔ %s   (/ping  /ip  /send <file>  /quit)", m.myName, m.peerName),
	)

	// Reserve lines for: header(1) + progress(0/1) + confirm(0/1) + input(3).
	extraLines := 0
	if m.progress != "" {
		extraLines++
	}
	if m.pendingConfirm {
		extraLines++
	}
	msgAreaHeight := m.height - 4 - extraLines
	if msgAreaHeight < 1 {
		msgAreaHeight = 1
	}

	var lines []string
	for _, msg := range m.messages {
		if msg.From == "" {
			lines = append(lines, sysStyle.Render("  "+msg.Body))
		} else {
			ts := timeStyle.Render(msg.At.Format("15:04"))
			var nameStr string
			if msg.From == m.myName {
				nameStr = myStyle.Render(msg.From)
			} else {
				nameStr = peerStyle.Render(msg.From)
			}
			lines = append(lines, fmt.Sprintf("%s %s: %s", ts, nameStr, msg.Body))
		}
	}

	if len(lines) > msgAreaHeight {
		lines = lines[len(lines)-msgAreaHeight:]
	}
	for len(lines) < msgAreaHeight {
		lines = append([]string{""}, lines...)
	}

	msgArea := strings.Join(lines, "\n")

	var promptLine string
	if m.pendingConfirm {
		promptLine = inputStyle.Width(m.width - 4).Render(
			confirmStyle.Render("Accept? [y/N]: ") + m.input + "█",
		)
	} else {
		promptLine = inputStyle.Width(m.width - 4).Render("> " + m.input + "█")
	}

	var errLine string
	if m.err != nil {
		errLine = errStyle.Render("Error: " + m.err.Error())
	}

	parts := []string{headerLine, msgArea}
	if m.progress != "" {
		parts = append(parts, sysStyle.Render("  "+m.progress))
	}
	if m.pendingConfirm {
		parts = append(parts, confirmStyle.Render("  "+m.confirmText))
	}
	if errLine != "" {
		parts = append(parts, errLine)
	}
	parts = append(parts, promptLine)

	return strings.Join(parts, "\n")
}
