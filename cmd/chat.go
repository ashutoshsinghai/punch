package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ashutoshsinghai/punch/internal/crypto"
	"github.com/ashutoshsinghai/punch/internal/filetransfer"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/stun"
	"github.com/ashutoshsinghai/punch/internal/transport"
	"github.com/ashutoshsinghai/punch/ui"
	tea "github.com/charmbracelet/bubbletea"
)

func runChat(result *punch.Result, session, myName, myPublicAddr string) error {
	cipher, err := crypto.NewCipher(session)
	if err != nil {
		return err
	}

	peerName := exchangeNames(result.Conn, cipher, myName)

	localAddr := myPublicAddr
	remoteAddr := result.Remote.String()

	// confirmCh receives the user's y/N decision for an incoming file offer.
	confirmCh := make(chan bool, 1)
	// ftAcceptCh delivers the peer's FT port on acceptance, or -1 on decline.
	ftAcceptCh := make(chan int, 1)

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

		case msg == "/ip", msg == "/info":
			prog.Send(ui.SystemMsg{
				Text: fmt.Sprintf("you: %s   peer: %s", localAddr, remoteAddr),
			})
			return nil

		case msg == "/geo":
			peerIP, _, _ := strings.Cut(remoteAddr, ":")
			go lookupGeo(peerIP, peerName, prog)
			return nil

		case strings.HasPrefix(msg, "/send "):
			path := strings.TrimSpace(strings.TrimPrefix(msg, "/send "))
			go chatSendFile(path, result.Conn, cipher, prog, ftAcceptCh, peerName, result.Remote.IP.String())
			return nil

		case msg == "__CONFIRM__:yes":
			confirmCh <- true
			return nil

		case msg == "__CONFIRM__:no":
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

			case strings.HasPrefix(text, "FILE_OFFER:"):
				// Spawn in a goroutine so the recv loop stays alive for chat
				// messages while the file transfer runs on its own connection.
				go handleIncomingFileOffer(text, result.Conn, cipher, prog, confirmCh, peerName, result.Remote.IP.String())

			case strings.HasPrefix(text, "FILE_ACCEPT:"):
				portStr := strings.TrimPrefix(text, "FILE_ACCEPT:")
				port, err := strconv.Atoi(portStr)
				if err != nil {
					port = -1
				}
				select {
				case ftAcceptCh <- port:
				default:
				}

			case text == "FILE_DECLINE":
				select {
				case ftAcceptCh <- -1:
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

// chatSendFile announces a file offer over the chat connection, waits for the
// peer to accept and share their file-transfer port, then opens a dedicated
// UDP connection and streams the file using the sliding-window protocol.
func chatSendFile(path string, chatConn *transport.Conn, cipher *crypto.Cipher, prog *tea.Program, ftAcceptCh chan int, peerName, peerChatIP string) {
	// Stat the file before doing anything.
	info, err := os.Stat(path)
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
		return
	}
	name := filepath.Base(path)
	total := info.Size()

	// Bind a dedicated UDP socket for the file transfer and discover its
	// public address via STUN so the peer can punch back to us.
	ftConn, err := punch.BindSocket()
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "send error (bind FT socket): " + err.Error()})
		return
	}
	defer ftConn.Close()

	_, ftPublicPort, err := stun.Discover(ftConn)
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "send error (STUN for FT): " + err.Error()})
		return
	}

	// Announce: FILE_OFFER:<size>:<sender-ft-port>:<name>
	// Name is last so that filenames containing ':' are parsed correctly.
	offer := fmt.Sprintf("FILE_OFFER:%d:%d:%s", total, ftPublicPort, name)
	enc, err := cipher.Encrypt([]byte(offer))
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
		return
	}
	if err := chatConn.Send(enc); err != nil {
		prog.Send(ui.SystemMsg{Text: "send error: " + err.Error()})
		return
	}

	prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("waiting for %s to accept %s (%s)...", peerName, name, humanBytes(int(total)))})

	// Keep the NAT mapping alive with periodic STUN pings while waiting for
	// the peer to accept. Most NATs expire idle UDP mappings after 30-60s.
	stopKeepalive := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				stun.Discover(ftConn) //nolint:errcheck
			case <-stopKeepalive:
				return
			}
		}
	}()

	// Wait for FILE_ACCEPT:<port> or FILE_DECLINE (signalled as -1).
	var peerFTPort int
	select {
	case peerFTPort = <-ftAcceptCh:
	case <-time.After(60 * time.Second):
		close(stopKeepalive)
		prog.Send(ui.ProgressMsg{})
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("no response from %s — transfer cancelled", peerName)})
		return
	}
	close(stopKeepalive)

	if peerFTPort < 0 {
		prog.Send(ui.ProgressMsg{})
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("%s declined %s", peerName, name)})
		return
	}

	peerFTAddr := &net.UDPAddr{IP: net.ParseIP(peerChatIP), Port: peerFTPort}

	// Punch the dedicated connection.
	prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("opening FT channel to %s...", peerName)})
	if err := punch.SoloPunch(ftConn, peerFTAddr); err != nil {
		prog.Send(ui.ProgressMsg{})
		prog.Send(ui.SystemMsg{Text: "FT punch failed: " + err.Error()})
		return
	}

	prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("sending %s (%s)...", name, humanBytes(int(total)))})

	ftStart := time.Now()
	err = filetransfer.Send(ftConn, peerFTAddr, path, cipher, func(sent, tot int64) {
		if tot == 0 {
			return
		}
		pct := int(sent * 100 / tot)
		prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("%s  %s  %d%%", progressBar(pct), name, pct)})
	})
	elapsed := time.Since(ftStart)
	prog.Send(ui.ProgressMsg{})
	if err != nil {
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("send error (%s): %s", name, err.Error())})
		filetransfer.Abort(ftConn, peerFTAddr)
		return
	}
	prog.Send(ui.SystemMsg{Text: fmt.Sprintf("sent %s (%s) in %s  —  %s/s",
		name, humanBytes(int(total)), elapsed.Round(time.Millisecond), humanBytes(int(float64(total)/elapsed.Seconds())))})
}

// handleIncomingFileOffer shows a confirmation prompt, waits for the user's decision,
// then opens a dedicated UDP connection and receives the file using the sliding-window protocol.
// Called in a goroutine so the main recv loop stays live for chat messages.
func handleIncomingFileOffer(offer string, chatConn *transport.Conn, cipher *crypto.Cipher, prog *tea.Program, confirmCh chan bool, peerName, peerChatIP string) {
	// FILE_OFFER:<size>:<sender-ft-port>:<name>
	// Name is last so filenames containing ':' parse correctly.
	parts := strings.SplitN(offer, ":", 4)
	if len(parts) != 4 {
		return
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return
	}
	senderFTPort, err := strconv.Atoi(parts[2])
	if err != nil {
		return
	}
	name := parts[3]

	prog.Send(ui.ConfirmMsg{
		Text: fmt.Sprintf("%s wants to send %s (%s)", peerName, name, humanBytes(int(size))),
	})

	// Block until the user responds (or times out).
	var accepted bool
	select {
	case accepted = <-confirmCh:
	case <-time.After(60 * time.Second):
		sendChatSignal(chatConn, cipher, "FILE_DECLINE")
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("transfer from %s timed out — auto-declined", peerName)})
		return
	}

	if !accepted {
		sendChatSignal(chatConn, cipher, "FILE_DECLINE")
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("declined %s from %s", name, peerName)})
		return
	}

	// Bind a dedicated FT socket and discover our public FT port.
	ftConn, err := punch.BindSocket()
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "FT accept error (bind): " + err.Error()})
		sendChatSignal(chatConn, cipher, "FILE_DECLINE")
		return
	}
	defer ftConn.Close()

	_, myFTPort, err := stun.Discover(ftConn)
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "FT accept error (STUN): " + err.Error()})
		sendChatSignal(chatConn, cipher, "FILE_DECLINE")
		return
	}

	// Tell sender our FT port so both sides can punch simultaneously.
	sendChatSignal(chatConn, cipher, fmt.Sprintf("FILE_ACCEPT:%d", myFTPort))

	senderFTAddr := &net.UDPAddr{IP: net.ParseIP(peerChatIP), Port: senderFTPort}

	// Punch the dedicated connection.
	prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("opening FT channel to %s...", peerName)})
	if err := punch.SoloPunch(ftConn, senderFTAddr); err != nil {
		prog.Send(ui.ProgressMsg{})
		prog.Send(ui.SystemMsg{Text: "FT punch failed: " + err.Error()})
		return
	}

	prog.Send(ui.SystemMsg{Text: fmt.Sprintf("receiving %s (%s)...", name, humanBytes(int(size)))})

	// Determine save path (cwd/name).
	savePath := name
	absPath, _ := filepath.Abs(savePath)

	ftStart := time.Now()
	err = filetransfer.Receive(ftConn, senderFTAddr, savePath, size, cipher, func(recv, tot int64) {
		if tot == 0 {
			return
		}
		pct := int(recv * 100 / tot)
		prog.Send(ui.ProgressMsg{Text: fmt.Sprintf("%s  %s  %d%%", progressBar(pct), name, pct)})
	})
	elapsed := time.Since(ftStart)
	prog.Send(ui.ProgressMsg{})
	if err != nil {
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("receive error (%s): %s", name, err.Error())})
		return
	}
	prog.Send(ui.SystemMsg{Text: fmt.Sprintf("saved %s → %s  (%s in %s  —  %s/s)",
		name, absPath, humanBytes(int(size)), elapsed.Round(time.Millisecond), humanBytes(int(float64(size)/elapsed.Seconds())))})
}

// sendChatSignal encrypts and sends a control string over the chat connection.
func sendChatSignal(conn *transport.Conn, cipher *crypto.Cipher, msg string) {
	enc, err := cipher.Encrypt([]byte(msg))
	if err != nil {
		return
	}
	conn.Send(enc) //nolint:errcheck
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

// lookupGeo calls ip-api.com (opt-in, triggered by /geo) to show peer location.
func lookupGeo(ip, peerName string, prog *tea.Program) {
	prog.Send(ui.SystemMsg{Text: fmt.Sprintf("looking up %s...", ip)})

	resp, err := http.Get("http://ip-api.com/json/" + ip + "?fields=status,message,country,countryCode,regionName,city,isp,org")
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "geo lookup failed: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		prog.Send(ui.SystemMsg{Text: "geo lookup failed: " + err.Error()})
		return
	}

	var result struct {
		Status     string `json:"status"`
		Message    string `json:"message"`
		City       string `json:"city"`
		RegionName string `json:"regionName"`
		Country    string `json:"country"`
		CountryCode string `json:"countryCode"`
		ISP        string `json:"isp"`
		Org        string `json:"org"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		prog.Send(ui.SystemMsg{Text: "geo lookup failed: " + err.Error()})
		return
	}
	if result.Status != "success" {
		prog.Send(ui.SystemMsg{Text: fmt.Sprintf("geo lookup: %s", result.Message)})
		return
	}

	location := fmt.Sprintf("%s, %s, %s", result.City, result.RegionName, result.CountryCode)
	provider := result.Org
	if provider == "" {
		provider = result.ISP
	}
	prog.Send(ui.SystemMsg{
		Text: fmt.Sprintf("%s (%s) — %s — %s", peerName, ip, location, provider),
	})
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
