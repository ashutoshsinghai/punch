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

	fmt.Fprintln(os.Stderr, "Discovering your public address via STUN...")

	conn, err := punch.BindSocket()
	if err != nil {
		return err
	}

	diag, err := stun.CheckNAT(conn)
	if err != nil {
		return fmt.Errorf("STUN discovery failed: %w", err)
	}
	publicIP, publicPort := diag.PublicIP, diag.PublicPort
	fmt.Fprintf(os.Stderr, "Your public address: %s:%d\n", publicIP, publicPort)
	printNATDiag(diag)

	payload, err := token.NewPayload(publicIP, publicPort)
	if err != nil {
		return fmt.Errorf("could not create token: %w", err)
	}

	tok, err := token.Encode(payload)
	if err != nil {
		return fmt.Errorf("could not encode token: %w", err)
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
		return fmt.Errorf("failed to read reply token: %w", err)
	}
	replyTok = strings.TrimSpace(replyTok)

	replyPayload, err := token.Decode(replyTok)
	if err != nil {
		return fmt.Errorf("invalid reply token: %w", err)
	}

	if replyPayload.Session != payload.Session {
		return fmt.Errorf("session mismatch — make sure your peer used your token")
	}

	remote := &net.UDPAddr{
		IP:   net.ParseIP(replyPayload.IP),
		Port: int(replyPayload.Port),
	}

	fmt.Fprintln(os.Stderr, "\nPunching through NAT...")

	result, err := punch.Simultaneous(conn, remote, func(msg string) {
		fmt.Fprintf(os.Stderr, "\r  %s        ", msg)
	})
	fmt.Fprintln(os.Stderr) // newline after progress line
	if err != nil {
		return punchError(err, diag)
	}

	fmt.Fprintln(os.Stderr, "Connected. Direct P2P. No server.\n")
	return runChat(result, payload.SessionHex(), myName, fmt.Sprintf("%s:%d", publicIP, publicPort))
}

// printNATDiag prints a one-line NAT status after STUN discovery.
// It warns clearly if CGNAT or symmetric NAT is detected so the user
// knows up-front why a connection might fail.
func printNATDiag(diag *stun.NATDiag) {
	switch {
	case diag.IsCGNAT:
		fmt.Fprintf(os.Stderr,
			"NAT: CGNAT detected (your ISP is doing an extra layer of NAT)\n"+
				"     IP %s is a shared ISP address — hole punching will likely fail.\n"+
				"     Try switching to a mobile hotspot.\n",
			diag.PublicIP)
	case diag.IsSymmetric:
		fmt.Fprintln(os.Stderr,
			"NAT: Symmetric NAT detected — hole punching may fail.\n"+
				"     Your router assigns a different port per destination.\n"+
				"     Try switching to a mobile hotspot.")
	default:
		fmt.Fprintln(os.Stderr, "NAT: OK — hole punching should work.")
	}
}

// punchError returns a descriptive error after Simultaneous times out,
// incorporating the local NAT diagnostic so the user sees the real reason.
func punchError(err error, diag *stun.NATDiag) error {
	switch {
	case diag.IsCGNAT:
		return fmt.Errorf(
			"connection failed: your ISP is using CGNAT (%s is not a true public IP).\n"+
				"Hole punching cannot work through two layers of ISP NAT.\n"+
				"Fix: switch to a mobile hotspot, or ask your ISP for a public IP.",
			diag.PublicIP)
	case diag.IsSymmetric:
		return fmt.Errorf(
			"connection failed: your router uses symmetric NAT.\n" +
				"Each new destination gets a different external port, so the peer\n" +
				"can't predict where to send packets.\n" +
				"Fix: switch to a mobile hotspot, or change your router's NAT mode to 'Full Cone'.")
	default:
		return fmt.Errorf(
			"connection failed: hole punch timed out.\n" +
				"Your NAT type looks OK — the peer's NAT may be the problem.\n" +
				"Ask them to run 'punch share' / 'punch join' and share their NAT status line.")
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
