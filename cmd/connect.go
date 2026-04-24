package cmd

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/spf13/cobra"
)

var connectPort int
var connectSession string

var connectCmd = &cobra.Command{
	Use:   "connect <peer-ip:port>",
	Short: "Connect directly to a peer at a known address (no token exchange)",
	Long: `Connect directly to a peer whose public ip:port you already know
(e.g. from DHT discovery). Both sides run this command simultaneously
with the same --session; there is no initiator/receiver distinction.

Since there is no token to carry the session, --session is required —
both peers must derive the same 2 session bytes from the same value
(hex literal or SHA-256 fallback, identical to 'punch share --session').`,
	Args: cobra.ExactArgs(1),
	RunE: runConnect,
}

func init() {
	connectCmd.Flags().IntVar(&connectPort, "port", 0, "Local UDP port to bind (0 = random; match whatever port your discovery layer announced for you)")
	connectCmd.Flags().StringVar(&connectSession, "session", "", "Shared session (hex → low 2 bytes literal, e.g. 823c → 0x82 0x3c; non-hex → SHA-256 first 2 bytes). REQUIRED.")
	_ = connectCmd.MarkFlagRequired("session")
	rootCmd.AddCommand(connectCmd)
}

func runConnect(_ *cobra.Command, args []string) error {
	fmt.Fprintln(os.Stderr)

	// ── Step 1: Parse peer address ────────────────────────────────────────────
	remote, err := net.ResolveUDPAddr("udp", strings.TrimSpace(args[0]))
	if err != nil {
		return stepFail("peer address", "invalid peer <ip:port>: "+err.Error())
	}
	if remote.IP == nil || remote.Port == 0 {
		return stepFail("peer address", "peer address must be <ip>:<port>")
	}
	stepOK("peer address", fmt.Sprintf("%s:%d", remote.IP, remote.Port))

	// ── Step 2: Derive session ────────────────────────────────────────────────
	s := strings.TrimSpace(connectSession)
	if s == "" {
		return stepFail("session", "--session is required")
	}
	session := deriveSession(s)
	sessionHex := fmt.Sprintf("%x", session)
	stepOK("session", "0x"+sessionHex)

	// ── Step 3: Bind local socket ─────────────────────────────────────────────
	conn, err := punch.BindSocket(connectPort)
	if err != nil {
		return stepFail("bind", err.Error())
	}
	localAddr := conn.LocalAddr().String()
	stepOK("bind", localAddr)
	if connectPort == 0 {
		fmt.Fprintf(os.Stderr, "      → warning: no --port set; peer can only reach you if their discovery layer learned this random port\n")
	}

	// ── Step 4: Hole punch ────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] hole punch\n")
	fmt.Fprintf(os.Stderr, "      → sending UDP probes to %s:%d every 200ms\n", remote.IP, remote.Port)
	fmt.Fprintf(os.Stderr, "      → peer must be running 'punch connect' at the same time with the same session\n")

	result, punchDiag, err := punch.Simultaneous(conn, remote, func(msg string) {
		fmt.Fprintf(os.Stderr, "\r      → %s        ", msg)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		printPacketDiag(punchDiag, fmt.Sprintf("%s:%d", remote.IP, remote.Port))
		return stepFail("hole punch", "timed out — is your peer running with the same --session and announcing this address?")
	}
	stepOK("hole punch", "connected — direct P2P, no server")

	fmt.Fprintln(os.Stderr)
	return runChat(result, sessionHex, "", localAddr)
}
