package cmd

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/stun"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var shareFormat string

var shareCmd = &cobra.Command{
	Use:   "share",
	Short: "Start a session and print a token for your peer",
	RunE:  runShare,
}

func init() {
	shareCmd.Flags().StringVar(&shareFormat, "token", "words", "Token format: words | base58")
	rootCmd.AddCommand(shareCmd)
}

func runShare(_ *cobra.Command, _ []string) error {
	myName := promptName()

	// ── Step 1: STUN ──────────────────────────────────────────────────────────
	step("STUN", "discovering your public address...")
	conn, err := punch.BindSocket()
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	diag, err := stun.CheckNAT(conn)
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	publicIP, publicPort := diag.PublicIP, diag.PublicPort
	stepOK("STUN", fmt.Sprintf("your address is %s:%d", publicIP, publicPort))

	// ── Step 2: NAT type ──────────────────────────────────────────────────────
	natLabel, natWarn := natDiagLine(diag)
	if natWarn {
		stepWarn("NAT type", natLabel)
	} else {
		stepOK("NAT type", natLabel)
	}

	// ── Step 3: Token exchange ────────────────────────────────────────────────
	payload, err := token.NewPayload(publicIP, publicPort)
	if err != nil {
		return stepFail("token", err.Error())
	}
	tok, err := token.Encode(payload)
	if err != nil {
		return stepFail("token", err.Error())
	}
	display := tok
	if shareFormat == "words" {
		display = token.Words(tok)
	}
	fmt.Printf("\nToken: %s\n", display)
	offerClipboard(display)
	fmt.Println("Send this to your peer over WhatsApp/Signal.\n")
	fmt.Print("Peer's reply token: ")

	reader := bufio.NewReader(os.Stdin)
	replyTok, err := reader.ReadString('\n')
	if err != nil {
		return stepFail("token", "could not read reply token: "+err.Error())
	}
	replyTok = strings.TrimSpace(replyTok)

	replyPayload, err := token.Decode(replyTok)
	if err != nil {
		return stepFail("token", "invalid reply token: "+err.Error())
	}
	if replyPayload.Session != payload.Session {
		return stepFail("token", "session mismatch — make sure your peer used your token")
	}
	stepOK("peer address", fmt.Sprintf("%s:%d", replyPayload.IP, replyPayload.Port))

	// ── Step 4: Hole punch ────────────────────────────────────────────────────
	remote := &net.UDPAddr{IP: net.ParseIP(replyPayload.IP), Port: int(replyPayload.Port)}
	step("hole punch", "probing peer...")

	elapsed := 0
	result, err := punch.Simultaneous(conn, remote, func(msg string) {
		elapsed++
		fmt.Fprintf(os.Stderr, "\r[   ] hole punch    — %s        ", msg)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return stepFail("hole punch", punchReason(diag))
	}
	stepOK("hole punch", "connected — direct P2P, no server")

	fmt.Fprintln(os.Stderr)
	return runChat(result, payload.SessionHex(), myName, fmt.Sprintf("%s:%d", publicIP, publicPort))
}

// ── Step output helpers ───────────────────────────────────────────────────────

func step(label, msg string) {
	fmt.Fprintf(os.Stderr, "[   ] %-12s— %s\n", label, msg)
}

func stepOK(label, msg string) {
	fmt.Fprintf(os.Stderr, "[ ✓ ] %-12s— %s\n", label, msg)
}

func stepWarn(label, msg string) {
	fmt.Fprintf(os.Stderr, "[ ! ] %-12s— %s\n", label, msg)
}

func stepFail(label, msg string) error {
	fmt.Fprintf(os.Stderr, "[ ✗ ] %-12s— %s\n", label, msg)
	return fmt.Errorf("%s: %s", label, msg)
}

// natDiagLine returns a one-line description of the NAT type and whether
// it should be shown as a warning.
func natDiagLine(diag *stun.NATDiag) (line string, warn bool) {
	switch {
	case diag.IsCGNAT:
		return fmt.Sprintf(
			"CGNAT — %s is a shared ISP address, hole punching will likely fail\n"+
				"              try switching to a mobile hotspot",
			diag.PublicIP), true
	case diag.IsSymmetric:
		return "symmetric NAT — router assigns a different port per destination, hole punching may fail\n" +
			"              try switching to a mobile hotspot or changing router NAT mode to Full Cone", true
	default:
		return "port-restricted NAT — hole punching should work", false
	}
}

// punchReason returns a plain string explaining why the punch likely failed,
// based on the local NAT diagnostic.
func punchReason(diag *stun.NATDiag) string {
	switch {
	case diag.IsCGNAT:
		return fmt.Sprintf(
			"timed out — your ISP is using CGNAT (%s is not a true public IP)\n"+
				"              fix: switch to a mobile hotspot, or ask your ISP for a public IP",
			diag.PublicIP)
	case diag.IsSymmetric:
		return "timed out — your router uses symmetric NAT\n" +
			"              fix: switch to a mobile hotspot, or set router NAT mode to Full Cone"
	default:
		return "timed out — your NAT looks OK, so the peer's network is likely the problem\n" +
			"              ask your peer to paste their full output so you can see their NAT status"
	}
}

// offerClipboard prompts the user to copy text to the clipboard.
// On systems without clipboard support (headless Linux without xclip/xsel/
// wl-clipboard), the prompt is skipped silently rather than showing a
// cryptic library error after the user has already pressed a key.
func offerClipboard(text string) {
	// Probe availability without touching the actual clipboard content.
	if _, err := clipboard.ReadAll(); err != nil {
		fmt.Println()
		return
	}
	fmt.Print("Press 'c' to copy to clipboard, any other key to skip: ")
	if key := readSingleKey(); key == 'c' || key == 'C' {
		if err := clipboard.WriteAll(text); err == nil {
			fmt.Println("copied!")
		} else {
			fmt.Println()
		}
	} else {
		fmt.Println()
	}
}

// readSingleKey reads one keypress without requiring Enter.
// Falls back to reading a full line if the terminal isn't a TTY.
func readSingleKey() byte {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
			buf := make([]byte, 1)
			os.Stdin.Read(buf) //nolint:errcheck
			return buf[0]
		}
	}
	// Fallback: read a line and return the first character.
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	if len(strings.TrimSpace(line)) > 0 {
		return line[0]
	}
	return 0
}
