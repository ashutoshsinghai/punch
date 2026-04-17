package cmd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ashutoshsinghai/punch/internal/crypto"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/transport"
	"github.com/ashutoshsinghai/punch/ui"
	tea "github.com/charmbracelet/bubbletea"
)

func runChat(result *punch.Result, session, myName string) error {
	cipher, err := crypto.NewCipher(session)
	if err != nil {
		return err
	}

	peerName := exchangeNames(result.Conn, cipher, myName)

	localAddr := result.Local.String()
	remoteAddr := result.Remote.String()

	// confirmCh receives the user's y/N decision for an incoming file request.
	confirmCh := make(chan bool, 1)
	// fileAckCh receives the peer's acceptance/decline of our outgoing file request.
	fileAckCh := make(chan bool, 1)

	var prog *tea.Program

	sendFn := func(msg string) error {
		switch {
		case msg == "/ping":
			go func() {
				ts := fmt.Sprintf("__PING__:%d", time.Now().UnixNano())
				enc, err := cipher.Encrypt([]byte(ts))
				if err != nil {
					return
				}
				result.Conn.Send(enc) //nolint:errcheck
			}()
			return nil

		case msg == "/ip":
			prog.Send(ui.SystemMsg{
				Text: fmt.Sprintf("you: %s   peer: %s", localAddr, remoteAddr),
			})
			return nil

		case strings.HasPrefix(msg, "/send "):
			path := strings.TrimSpace(strings.TrimPrefix(msg, "/send "))
			go chatSendFile(path, result.Conn, cipher, prog, fileAckCh, peerName)
			return nil

		case msg == "__CONFIRM__:yes":
			enc, err := cipher.Encrypt([]byte("FILE_ACK:yes"))
			if err != nil {
				return err
			}
			if err := result.Conn.Send(enc); err != nil {
				return err
			}
			confirmCh <- true
			return nil

		case msg == "__CONFIRM__:no":
			enc, err := cipher.Encrypt([]byte("FILE_ACK:no"))
			if err != nil {
				return err
			}
			if err := result.Conn.Send(enc); err != nil {
				return err
			}
			confirmCh <- false
			return nil
		}

		enc, err := cipher.Encrypt([]byte(msg))
		if err != nil {
			return err
		}
		return result.Conn.Send(enc)
	}

	prog = ui.NewChat(myName, peerName, sendFn)

	// Single recv goroutine — only one caller of conn.Recv() at a time.
	go func() {
		for {
			raw, err := result.Conn.Recv()
			if err != nil {
				return
			}
			plain, err := cipher.Decrypt(raw)
			if err != nil {
				continue
			}
			text := string(plain)

			switch {
			case strings.HasPrefix(text, "__PING__:"):
				go func(t string) {
					pong := strings.Replace(t, "__PING__:", "__PONG__:", 1)
					enc, err := cipher.Encrypt([]byte(pong))
					if err != nil {
						return
					}
					result.Conn.Send(enc) //nolint:errcheck
				}(text)

			case strings.HasPrefix(text, "__PONG__:"):
				nanos, err := strconv.ParseInt(strings.TrimPrefix(text, "__PONG__:"), 10, 64)
				if err != nil {
					continue
				}
				rtt := time.Since(time.Unix(0, nanos))
				prog.Send(ui.SystemMsg{Text: fmt.Sprintf("pong: %s", rtt.Round(time.Millisecond))})

			case strings.HasPrefix(text, "FILE_REQ:"):
				// Blocks recv loop until user responds and file is received (or declined).
				handleIncomingFileReq(text, result.Conn, cipher, prog, confirmCh, peerName)

			case strings.HasPrefix(text, "FILE_ACK:"):
				accepted := text == "FILE_ACK:yes"
				select {
				case fileAckCh <- accepted:
				default:
				}

			default:
				prog.Send(ui.IncomingMsg{From: peerName, Body: text})
			}
		}
	}()

	if _, err := prog.Run(); err != nil && err != tea.ErrProgramKilled {
		return err
	}
	result.Conn.Close()
	return nil
}

// chatSendFile reads a file, sends a FILE_REQ, waits for peer acceptance, then streams chunks.
func chatSendFile(path string, conn *transport.Conn, cipher *crypto.Cipher, prog *tea.Program, ackCh chan bool, peerName string) {
	data, err := os.ReadFile(path)
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
		return
	}

	name := filepath.Base(path)
	hash := sha256.Sum256(data)
	hashHex := hex.EncodeToString(hash[:])
	total := len(data)

	// Send the request with metadata — no data yet.
	req := fmt.Sprintf("FILE_REQ:%s:%d:%s", name, total, hashHex)
	enc, err := cipher.Encrypt([]byte(req))
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
		return
	}
	if err := conn.Send(enc); err != nil {
		prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
		return
	}

	prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("waiting for %s to accept %s (%s)...", peerName, name, humanBytes(total))})

	// Wait for peer's accept/decline (or timeout).
	var accepted bool
	select {
	case accepted = <-ackCh:
	case <-time.After(60 * time.Second):
		prog.Send(ui.ProgressMsg{})
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("no response from %s — transfer cancelled", peerName)})
		return
	}

	if !accepted {
		prog.Send(ui.ProgressMsg{})
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("%s declined %s", peerName, name)})
		return
	}

	// Accepted — stream the file.
	const chunkSize = 8192
	sent := 0

	for offset := 0; offset < total; offset += chunkSize {
		end := offset + chunkSize
		if end > total {
			end = total
		}
		enc, err := cipher.Encrypt(data[offset:end])
		if err != nil {
			prog.Send(ui.ProgressMsg{})
			prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
			return
		}
		if err := conn.Send(enc); err != nil {
			prog.Send(ui.ProgressMsg{})
			prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
			return
		}
		sent = end
		pct := sent * 100 / total
		prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("%s  %s  %d%%", progressBar(pct), name, pct)})
	}

	eofEnc, err := cipher.Encrypt([]byte("EOF"))
	if err != nil {
		prog.Send(ui.ProgressMsg{})
		prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
		return
	}
	if err := conn.Send(eofEnc); err != nil {
		prog.Send(ui.ProgressMsg{})
		prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
		return
	}

	prog.Send(ui.ProgressMsg{})
	prog.Send(ui.SystemMsg{Text: fmt.Sprintf("sent %s (%s)", name, humanBytes(total))})
}

// handleIncomingFileReq shows a confirmation prompt, waits for the user's decision,
// then either receives the file or tells the sender it was declined.
// Called synchronously from the recv goroutine.
func handleIncomingFileReq(req string, conn *transport.Conn, cipher *crypto.Cipher, prog *tea.Program, confirmCh chan bool, peerName string) {
	parts := strings.SplitN(req, ":", 4)
	if len(parts) != 4 {
		return
	}
	name := parts[1]
	size, err := strconv.Atoi(parts[2])
	if err != nil {
		return
	}
	expectedHash := parts[3]

	prog.Send(ui.ConfirmMsg{
		Text: fmt.Sprintf("%s wants to send %s (%s)", peerName, name, humanBytes(size)),
	})

	// Block until the user responds (or times out).
	var accepted bool
	select {
	case accepted = <-confirmCh:
	case <-time.After(60 * time.Second):
		// Auto-decline on timeout.
		enc, _ := cipher.Encrypt([]byte("FILE_ACK:no"))
		conn.Send(enc) //nolint:errcheck
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("transfer from %s timed out — auto-declined", peerName)})
		return
	}

	if !accepted {
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("declined %s from %s", name, peerName)})
		return
	}

	// Receive the file.
	prog.Send(ui.SystemMsg{Text: fmt.Sprintf("receiving %s (%s)...", name, humanBytes(size))})

	var fileData []byte

	for {
		raw, err := conn.Recv()
		if err != nil {
			prog.Send(ui.ProgressMsg{})
			prog.Send(ui.SystemMsg{Text: "receive error: " + err.Error()})
			return
		}
		plain, err := cipher.Decrypt(raw)
		if err != nil {
			prog.Send(ui.ProgressMsg{})
			prog.Send(ui.SystemMsg{Text: "decrypt error: " + err.Error()})
			return
		}
		if string(plain) == "EOF" {
			break
		}
		fileData = append(fileData, plain...)
		if size > 0 {
			pct := len(fileData) * 100 / size
			prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("%s  %s  %d%%", progressBar(pct), name, pct)})
		}
	}

	prog.Send(ui.ProgressMsg{})

	got := sha256.Sum256(fileData)
	if hex.EncodeToString(got[:]) != expectedHash {
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("hash mismatch for %s — file may be corrupted", name)})
		return
	}

	if err := os.WriteFile(name, fileData, 0644); err != nil {
		prog.Send(ui.SystemMsg{Text: "save error: " + err.Error()})
		return
	}

	absPath, err := filepath.Abs(name)
	if err != nil {
		absPath = name
	}
	prog.Send(ui.SystemMsg{Text: fmt.Sprintf("saved %s → %s", name, absPath)})
}

// exchangeNames performs a best-effort name handshake immediately after connecting.
func exchangeNames(conn *transport.Conn, cipher *crypto.Cipher, myName string) string {
	go func() {
		enc, err := cipher.Encrypt([]byte("__NAME__:" + myName))
		if err != nil {
			return
		}
		conn.Send(enc) //nolint:errcheck
	}()

	ch := make(chan string, 1)
	go func() {
		raw, err := conn.Recv()
		if err != nil {
			ch <- "peer"
			return
		}
		plain, err := cipher.Decrypt(raw)
		if err != nil {
			ch <- "peer"
			return
		}
		text := string(plain)
		if strings.HasPrefix(text, "__NAME__:") {
			name := strings.TrimPrefix(text, "__NAME__:")
			if name == "" {
				name = "peer"
			}
			ch <- name
		} else {
			ch <- "peer"
		}
	}()

	select {
	case name := <-ch:
		return name
	case <-time.After(5 * time.Second):
		return "peer"
	}
}

// promptName asks the user for their display name, falling back to the OS username.
func promptName() string {
	fmt.Print("Your name: ")
	reader := bufio.NewReader(os.Stdin)
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		return localUsername()
	}
	return name
}

// progressBar returns a 20-char filled/empty bar for the given percentage.
func progressBar(pct int) string {
	if pct > 100 {
		pct = 100
	}
	filled := pct / 5
	bar := make([]rune, 20)
	for i := range bar {
		if i < filled {
			bar[i] = '█'
		} else {
			bar[i] = '░'
		}
	}
	return string(bar)
}

func localUsername() string {
	u, err := user.Current()
	if err != nil {
		return "me"
	}
	if u.Username != "" {
		return u.Username
	}
	return "me"
}
