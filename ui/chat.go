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

// knownCommands is the set of recognised slash-commands (prefix match for /send).
var knownCommands = []string{"/quit", "/exit", "/ping", "/ip", "/info", "/geo", "/send ", "/clear", "/help", "/ls"}

func isKnownCommand(text string) bool {
	for _, c := range knownCommands {
		if strings.HasPrefix(c, "/send") {
			if strings.HasPrefix(text, c) {
				return true
			}
		} else if text == c {
			return true
		}
	}
	return false
}

// model is the Bubbletea model for the chat TUI.
type model struct {
	messages       []Message
	inputRunes     []rune
	cursor         int    // rune index in inputRunes
	history        []string // sent messages/commands, oldest first
	historyIdx     int    // index into history while browsing; -1 = not browsing
	savedInput     []rune // input saved when history browsing starts
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

// ProgressMsg updates the live transfer progress bar. Empty Text clears it.
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
		myName:     myName,
		peerName:   peerName,
		send:       send,
		historyIdx: -1,
	}
	return tea.NewProgram(m, tea.WithAltScreen())
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) inputText() string { return string(m.inputRunes) }

// insertRune inserts r at the current cursor position and advances the cursor.
func (m *model) insertRune(r rune) {
	m.inputRunes = append(m.inputRunes[:m.cursor],
		append([]rune{r}, m.inputRunes[m.cursor:]...)...)
	m.cursor++
}

// deleteBeforeCursor removes the rune immediately before the cursor.
func (m *model) deleteBeforeCursor() {
	if m.cursor == 0 {
		return
	}
	m.inputRunes = append(m.inputRunes[:m.cursor-1], m.inputRunes[m.cursor:]...)
	m.cursor--
}

// deleteAtCursor removes the rune at the cursor (forward delete).
func (m *model) deleteAtCursor() {
	if m.cursor >= len(m.inputRunes) {
		return
	}
	m.inputRunes = append(m.inputRunes[:m.cursor], m.inputRunes[m.cursor+1:]...)
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

		// --- cursor movement ---
		case tea.KeyLeft:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyRight:
			if m.cursor < len(m.inputRunes) {
				m.cursor++
			}
		case tea.KeyHome, tea.KeyCtrlA:
			m.cursor = 0
		case tea.KeyEnd, tea.KeyCtrlE:
			m.cursor = len(m.inputRunes)

		// --- history navigation ---
		case tea.KeyUp:
			if len(m.history) == 0 {
				break
			}
			if m.historyIdx == -1 {
				// Start browsing: save current input
				m.savedInput = append([]rune{}, m.inputRunes...)
				m.historyIdx = len(m.history) - 1
			} else if m.historyIdx > 0 {
				m.historyIdx--
			}
			m.inputRunes = []rune(m.history[m.historyIdx])
			m.cursor = len(m.inputRunes)

		case tea.KeyDown:
			if m.historyIdx == -1 {
				break
			}
			if m.historyIdx < len(m.history)-1 {
				m.historyIdx++
				m.inputRunes = []rune(m.history[m.historyIdx])
			} else {
				// Back to current input
				m.historyIdx = -1
				m.inputRunes = append([]rune{}, m.savedInput...)
			}
			m.cursor = len(m.inputRunes)

		// --- editing ---
		case tea.KeyBackspace, tea.KeyDelete:
			if msg.Type == tea.KeyDelete {
				m.deleteAtCursor()
			} else {
				m.deleteBeforeCursor()
			}

		case tea.KeySpace:
			m.insertRune(' ')

		case tea.KeyEnter:
			text := strings.TrimSpace(m.inputText())
			m.inputRunes = m.inputRunes[:0]
			m.cursor = 0

			if m.pendingConfirm {
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

			// /clear — wipe the message area locally, no network send.
			if text == "/clear" {
				m.messages = m.messages[:0]
				return m, nil
			}

			// /help — show command list locally.
			if text == "/help" {
				help := []string{
					"/ping          — measure round-trip time to peer",
					"/ip  /info     — show your and peer's public address",
					"/geo           — look up peer's location",
					"/send <file>   — send a file to peer",
					"/ls            — list files in current directory",
					"/clear         — clear the chat window",
					"/help          — show this help",
					"/quit          — exit punch",
				}
				for _, line := range help {
					m.messages = append(m.messages, Message{Body: line, At: time.Now()})
				}
				return m, nil
			}

			// Unknown slash command — show error, don't send.
			if strings.HasPrefix(text, "/") && !isKnownCommand(text) {
				m.messages = append(m.messages, Message{
					Body: fmt.Sprintf("unknown command: %s  (try /help)", text),
					At:   time.Now(),
				})
				return m, nil
			}

			// Add to history (skip duplicates of last entry).
			if len(m.history) == 0 || m.history[len(m.history)-1] != text {
				m.history = append(m.history, text)
			}
			m.historyIdx = -1
			m.savedInput = m.savedInput[:0]

			// Known commands handled by cmd layer — don't echo locally.
			isCmd := text == "/ping" || text == "/ip" || text == "/info" || text == "/geo" ||
				strings.HasPrefix(text, "/send ") || text == "/ls"
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

		default:
			if msg.Type == tea.KeyRunes {
				for _, r := range msg.Runes {
					m.insertRune(r)
				}
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
		fmt.Sprintf("punch — %s ↔ %s   (/help for commands)", m.myName, m.peerName),
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

	// Render input with cursor block inserted at cursor position.
	before := string(m.inputRunes[:m.cursor])
	after := string(m.inputRunes[m.cursor:])
	inputWithCursor := before + "█" + after

	var promptLine string
	if m.pendingConfirm {
		promptLine = inputStyle.Width(m.width - 4).Render(
			confirmStyle.Render("Accept? [y/N]: ") + inputWithCursor,
		)
	} else {
		promptLine = inputStyle.Width(m.width - 4).Render("> " + inputWithCursor)
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
