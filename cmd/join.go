package cmd

import (
	"fmt"
	"net"
	"os"

	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/stun"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var joinPort int

var joinCmd = &cobra.Command{
	Use:   "join <token>",
	Short: "Connect to a peer using their token",
	Args:  cobra.ExactArgs(1),
	RunE:  runJoin,
}

func init() {
	joinCmd.Flags().IntVar(&joinPort, "port", 0, "Local UDP port to bind (0 = random, try 3478 if direct connection fails)")
	rootCmd.AddCommand(joinCmd)
}

func runJoin(_ *cobra.Command, args []string) error {
	fmt.Fprintln(os.Stderr)

	// ── Step 1: Decode peer's token ───────────────────────────────────────────
	rawToken := args[0]
	fmt.Fprintf(os.Stderr, "[   ] peer address\n")
	fmt.Fprintf(os.Stderr, "      → decoding token...\n")
	payload, err := token.Decode(rawToken)
	if err != nil {
		return stepFail("peer address", "invalid token — "+err.Error())
	}
	fmt.Fprintf(os.Stderr, "      → peer's public address: %s:%d\n", payload.IP, payload.Port)
	stepOK("peer address", fmt.Sprintf("%s:%d", payload.IP, payload.Port))

	// ── Step 2: STUN ──────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] STUN\n")
	fmt.Fprintf(os.Stderr, "      → querying %s...\n", stun.Server)
	conn, err := punch.BindSocket(joinPort)
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	diag, err := stun.CheckNAT(conn)
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	myPublicIP, myPublicPort := diag.PublicIP, diag.PublicPort
	fmt.Fprintf(os.Stderr, "      → your public address: %s:%d\n", myPublicIP, myPublicPort)
	fmt.Fprintf(os.Stderr, "        (this is what the internet sees for your UDP socket)\n")
	stepOK("STUN", "")

	// ── Step 3: NAT type ──────────────────────────────────────────────────────
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
		fmt.Fprintf(os.Stderr, "      → %s is in the CGNAT range (RFC 6598: 100.64.0.0/10)\n", myPublicIP)
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

	// ── Step 4: Reply token ───────────────────────────────────────────────────
	replyPayload, err := token.NewReplyPayload(myPublicIP, myPublicPort, payload.Session)
	if err != nil {
		return stepFail("reply token", err.Error())
	}
	replyTok, err := token.Encode(replyPayload)
	if err != nil {
		return stepFail("reply token", err.Error())
	}
	replyWords := token.Words(replyTok)
	fmt.Printf("\nReply token: %s\n", replyWords)
	offerClipboard(replyWords)
	fmt.Println("Send this back to your peer over WhatsApp/Signal.")

	// ── Step 5: Hole punch ────────────────────────────────────────────────────
	remote := &net.UDPAddr{IP: net.ParseIP(payload.IP), Port: int(payload.Port)}
	fmt.Fprintf(os.Stderr, "\n[   ] hole punch\n")
	fmt.Fprintf(os.Stderr, "      → sending UDP probes to %s:%d every 200ms\n", payload.IP, payload.Port)

	result, punchDiag, err := punch.Simultaneous(conn, remote, func(msg string) {
		fmt.Fprintf(os.Stderr, "\r      → %s        ", msg)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		printPunchFailReason(diag)
		printPacketDiag(punchDiag, fmt.Sprintf("%s:%d", payload.IP, payload.Port))
		return stepFail("hole punch", "timed out — see diagnosis above")
	}
	stepOK("hole punch", "connected — direct P2P, no server")

	fmt.Fprintln(os.Stderr)
	return runChat(result, payload.SessionHex(), "", fmt.Sprintf("%s:%d", myPublicIP, myPublicPort))
}
