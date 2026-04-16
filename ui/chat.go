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
	messages []Message
	input    string
	myName   string
	peerName string
	send     SendFn
	err      error
	width    int
	height   int
}

// IncomingMsg is a Bubbletea message for a message received from the peer.
type IncomingMsg struct {
	From string
	Body string
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
			if text == "" {
				return m, nil
			}
			if text == "/quit" || text == "/exit" {
				return m, tea.Quit
			}
			m.messages = append(m.messages, Message{
				From: m.myName,
				Body: text,
				At:   time.Now(),
			})
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
		fmt.Sprintf("punch — connected to %s   (Ctrl+C or /quit to exit)", m.peerName),
	)

	// Message area: everything except header (1 line) + input (3 lines).
	msgAreaHeight := m.height - 4
	if msgAreaHeight < 1 {
		msgAreaHeight = 1
	}

	var lines []string
	for _, msg := range m.messages {
		ts := timeStyle.Render(msg.At.Format("15:04"))
		var nameStr string
		if msg.From == m.myName {
			nameStr = myStyle.Render(msg.From)
		} else {
			nameStr = peerStyle.Render(msg.From)
		}
		lines = append(lines, fmt.Sprintf("%s %s: %s", ts, nameStr, msg.Body))
	}

	// Show only the last msgAreaHeight lines.
	if len(lines) > msgAreaHeight {
		lines = lines[len(lines)-msgAreaHeight:]
	}

	// Pad to fill the area.
	for len(lines) < msgAreaHeight {
		lines = append([]string{""}, lines...)
	}

	msgArea := strings.Join(lines, "\n")

	var errLine string
	if m.err != nil {
		errLine = errStyle.Render("Error: " + m.err.Error())
	}

	prompt := inputStyle.Width(m.width - 4).Render("> " + m.input + "█")

	parts := []string{headerLine, msgArea}
	if errLine != "" {
		parts = append(parts, errLine)
	}
	parts = append(parts, prompt)

	return strings.Join(parts, "\n")
}
