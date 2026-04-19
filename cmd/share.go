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
	fmt.Fprintln(os.Stderr)

	// ── Step 1: STUN ──────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] STUN\n")
	fmt.Fprintf(os.Stderr, "      → querying %s...\n", stun.Server)
	conn, err := punch.BindSocket()
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	diag, err := stun.CheckNAT(conn)
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	publicIP, publicPort := diag.PublicIP, diag.PublicPort
	fmt.Fprintf(os.Stderr, "      → your public address: %s:%d\n", publicIP, publicPort)
	fmt.Fprintf(os.Stderr, "        (this is what the internet sees for your UDP socket)\n")
	stepOK("STUN", "")

	// ── Step 2: NAT type ──────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] NAT type\n")
	fmt.Fprintf(os.Stderr, "      → server 1 (%s)  mapped port: %d\n", stun.Server, diag.PublicPort)
	if diag.PublicPort2 != 0 {
		sameOrDiff := "same ✓"
		if diag.IsSymmetric {
			sameOrDiff = "DIFFERENT ✗"
		}
		fmt.Fprintf(os.Stderr, "      → server 2 (%s) mapped port: %d  (%s)\n",
			stun.Server2, diag.PublicPort2, sameOrDiff)
	} else {
		fmt.Fprintf(os.Stderr, "      → server 2 query failed (skipping symmetric NAT check)\n")
	}
	if diag.IsCGNAT {
		fmt.Fprintf(os.Stderr, "      → %s is in the CGNAT range (RFC 6598: 100.64.0.0/10)\n", publicIP)
		fmt.Fprintf(os.Stderr, "        your ISP has put you behind their own NAT — two NATs between you and the internet\n")
		fmt.Fprintf(os.Stderr, "        UDP hole punching cannot reliably work through double NAT\n")
		fmt.Fprintf(os.Stderr, "        tip: switch to a mobile hotspot\n")
		stepWarn("NAT type", "CGNAT detected")
	} else if diag.IsSymmetric {
		fmt.Fprintf(os.Stderr, "      → your router assigns a different external port per destination\n")
		fmt.Fprintf(os.Stderr, "        the peer cannot predict which port to send packets back to\n")
		fmt.Fprintf(os.Stderr, "        tip: switch to a mobile hotspot or set router NAT mode to Full Cone\n")
		stepWarn("NAT type", "symmetric NAT detected")
	} else {
		fmt.Fprintf(os.Stderr, "      → both servers see the same port — NAT is not symmetric\n")
		stepOK("NAT type", "port-restricted, hole punching should work")
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
	fmt.Fprintf(os.Stderr, "\n[   ] peer address\n")
	fmt.Fprintf(os.Stderr, "      → decoded from reply token: %s:%d\n", replyPayload.IP, replyPayload.Port)
	stepOK("peer address", fmt.Sprintf("%s:%d", replyPayload.IP, replyPayload.Port))

	// ── Step 4: Hole punch ────────────────────────────────────────────────────
	remote := &net.UDPAddr{IP: net.ParseIP(replyPayload.IP), Port: int(replyPayload.Port)}
	fmt.Fprintf(os.Stderr, "[   ] hole punch\n")
	fmt.Fprintf(os.Stderr, "      → sending UDP probes to %s:%d every 200ms\n", replyPayload.IP, replyPayload.Port)

	result, err := punch.Simultaneous(conn, remote, func(msg string) {
		fmt.Fprintf(os.Stderr, "\r      → %s        ", msg)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		printPunchFailReason(diag)
		return stepFail("hole punch", "timed out — see diagnosis above")
	}
	stepOK("hole punch", "connected — direct P2P, no server")

	fmt.Fprintln(os.Stderr)
	return runChat(result, payload.SessionHex(), myName, fmt.Sprintf("%s:%d", publicIP, publicPort))
}

// ── Step output helpers ───────────────────────────────────────────────────────

func stepOK(label, msg string) {
	if msg != "" {
		fmt.Fprintf(os.Stderr, "[ ✓ ] %s — %s\n", label, msg)
	} else {
		fmt.Fprintf(os.Stderr, "[ ✓ ] %s\n", label)
	}
}

func stepWarn(label, msg string) {
	fmt.Fprintf(os.Stderr, "[ ! ] %s — %s\n", label, msg)
}

func stepFail(label, msg string) error {
	fmt.Fprintf(os.Stderr, "[ ✗ ] %s — %s\n", label, msg)
	return fmt.Errorf("%s failed", label)
}

// printPunchFailReason prints a detailed diagnosis block when hole punching
// times out, so the user (or the person they share it with) can pinpoint the cause.
func printPunchFailReason(diag *stun.NATDiag) {
	fmt.Fprintln(os.Stderr, "      → diagnosis:")
	switch {
	case diag.IsCGNAT:
		fmt.Fprintf(os.Stderr,
			"        your ISP is using CGNAT — %s is not a true public IP\n"+
				"        packets punched to this address never reach your router\n"+
				"        fix: switch to a mobile hotspot (usually gets a real public IP)\n"+
				"             or call your ISP and ask for a static/public IP\n",
			diag.PublicIP)
	case diag.IsSymmetric:
		fmt.Fprintf(os.Stderr,
			"        your router uses symmetric NAT\n"+
				"        server 1 saw port %d, server 2 saw port %d — they differ\n"+
				"        the peer aimed at port %d but your router used a different port\n"+
				"        fix: switch to a mobile hotspot\n"+
				"             or change your router's NAT type to Full Cone / Endpoint-Independent\n",
			diag.PublicPort, diag.PublicPort2, diag.PublicPort)
	default:
		fmt.Fprintln(os.Stderr,
			"        your NAT looks fine (port-restricted, not symmetric, not CGNAT)\n"+
				"        the peer's network is likely the problem\n"+
				"        ask your peer to paste their full output — look at their NAT type lines")
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
