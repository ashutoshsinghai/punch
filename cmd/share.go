package cmd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/stun"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var shareFormat string
var sharePort int
var shareSession string

var shareCmd = &cobra.Command{
	Use:   "share",
	Short: "Start a session and print a token for your peer",
	RunE:  runShare,
}

func init() {
	shareCmd.Flags().StringVar(&shareFormat, "token", "words", "Token format: words | base58")
	shareCmd.Flags().IntVar(&sharePort, "port", 0, "Local UDP port to bind (0 = random, try 3478 if direct connection fails)")
	shareCmd.Flags().StringVar(&shareSession, "session", "", "Deterministic session (hex → low 2 bytes literal, e.g. 823c → 0x82 0x3c; non-hex → SHA-256 first 2 bytes)")
	rootCmd.AddCommand(shareCmd)
}

func runShare(_ *cobra.Command, _ []string) error {
	fmt.Fprintln(os.Stderr)

	// ── Step 1: STUN ──────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] STUN\n")
	fmt.Fprintf(os.Stderr, "      → querying %s...\n", stun.Server)
	conn, err := punch.BindSocket(sharePort)
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

	// ── Verdict ───────────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] verdict\n")
	if diag.IsCGNAT || diag.IsSymmetric {
		fmt.Fprintf(os.Stderr, "      → your network will likely block hole punching\n")
		fmt.Fprintf(os.Stderr, "      → proceeding anyway — switch to a mobile hotspot if it fails\n")
		stepWarn("verdict", "likely to fail on your side")
	} else {
		fmt.Fprintf(os.Stderr, "      → your network supports direct P2P connections\n")
		stepOK("verdict", "your side is ready")
	}

	// ── Step 3: Token exchange ────────────────────────────────────────────────
	var payload token.Payload
	if s := strings.TrimSpace(shareSession); s != "" {
		payload = token.Payload{IP: publicIP, Port: publicPort, Session: deriveSession(s)}
	} else {
		payload, err = token.NewPayload(publicIP, publicPort)
		if err != nil {
			return stepFail("token", err.Error())
		}
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

	// Keep the NAT mapping alive while waiting for the reply token.
	// Without this, NATs typically expire the UDP mapping after 30–60 s of
	// idle, assigning a new external port when hole punching starts — making
	// the address we encoded in the token stale and unreachable by the peer.
	stunAddr, _ := net.ResolveUDPAddr("udp", stun.Server)
	keepaliveDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				conn.WriteTo([]byte{0}, stunAddr) //nolint:errcheck // best-effort keepalive
			case <-keepaliveDone:
				return
			}
		}
	}()

	reader := bufio.NewReader(os.Stdin)
	replyTok, err := reader.ReadString('\n')
	close(keepaliveDone)
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

	result, punchDiag, err := punch.Simultaneous(conn, remote, func(msg string) {
		fmt.Fprintf(os.Stderr, "\r      → %s        ", msg)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		printPunchFailReason(diag)
		printPacketDiag(punchDiag, fmt.Sprintf("%s:%d", replyPayload.IP, replyPayload.Port))
		return stepFail("hole punch", "timed out — see diagnosis above")
	}
	stepOK("hole punch", "connected — direct P2P, no server")

	fmt.Fprintln(os.Stderr)
	return runChat(result, payload.SessionHex(), "", fmt.Sprintf("%s:%d", publicIP, publicPort))
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

// printPacketDiag prints what the socket actually received during hole punching,
// so the user can tell whether packets are being dropped by the ISP or whether
// there is a port mismatch.
func printPacketDiag(d *punch.PunchDiag, expectedAddr string) {
	fmt.Fprintln(os.Stderr, "      → packet diagnostic:")
	if d.TotalReceived == 0 {
		fmt.Fprintln(os.Stderr,
			"        no UDP packets received at all\n"+
				"        your ISP or the peer's ISP is dropping the traffic\n"+
				"        this is common on Indian residential connections (Jio, Airtel, BSNL)\n"+
				"        try: switch to a mobile hotspot — mobile data often bypasses this filter")
	} else if d.WrongSource > 0 {
		fmt.Fprintf(os.Stderr,
			"        received %d packet(s) but from wrong address\n"+
				"        expected: %s\n"+
				"        got:      %s\n"+
				"        your peer's NAT is remapping their port — symmetric NAT behaviour\n"+
				"        try: switch to a mobile hotspot or set router NAT mode to Full Cone\n",
			d.WrongSource, expectedAddr, d.WrongSourceIP)
	} else {
		fmt.Fprintf(os.Stderr,
			"        received %d packet(s) from the right address but handshake never completed\n"+
				"        this is unusual — please file a bug at https://github.com/ashutoshsinghai/punch\n",
			d.TotalReceived)
	}
}

// offerClipboard is a no-op — clipboard interaction has been removed.
// The token is printed on screen; users copy it manually.
func offerClipboard(_ string) {}

// deriveSession turns --session input into 2 session bytes.
//
// Hex inputs are taken literally (low-order 2 bytes): "823c" → 0x82 0x3c,
// "ff" → 0x00 0xff, "deadbeef" → 0xbe 0xef. Non-hex inputs fall back to
// SHA-256 and take the first 2 bytes.
func deriveSession(s string) [2]byte {
	var session [2]byte
	padded := s
	if len(padded)%2 == 1 {
		padded = "0" + padded
	}
	if b, err := hex.DecodeString(padded); err == nil && len(b) > 0 {
		if len(b) >= 2 {
			copy(session[:], b[len(b)-2:])
		} else {
			session[1] = b[0]
		}
		return session
	}
	h := sha256.Sum256([]byte(s))
	copy(session[:], h[:2])
	return session
}

