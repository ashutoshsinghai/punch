package cmd

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ashutoshsinghai/punch/internal/probe"
	"github.com/spf13/cobra"
)

var probeCmd = &cobra.Command{
	Use:   "probe [token]",
	Short: "Test which ports can reach your peer (run on both sides simultaneously)",
	Long: `punch probe checks whether a direct UDP connection is possible between you
and your peer, and which port works best — without establishing a full session.

Unlike nmap, both sides probe simultaneously, matching the real hole-punch
behaviour of punch share/join.

Run with no arguments to generate a probe token, share it with your peer,
and they run: punch probe <token>`,
	Args: cobra.MaximumNArgs(1),
	RunE: runProbe,
}

func init() {
	rootCmd.AddCommand(probeCmd)
}

func runProbe(_ *cobra.Command, args []string) error {
	fmt.Fprintln(os.Stderr)
	if len(args) == 0 {
		return runProbeInitiator()
	}
	return runProbeResponder(args[0])
}

// ── Initiator ─────────────────────────────────────────────────────────────────

func runProbeInitiator() error {
	sockets, err := bindAndShow()
	if err != nil {
		return err
	}
	defer probe.CloseAll(sockets)

	// Build and print probe token.
	session, err := probe.NewSession()
	if err != nil {
		return stepFail("probe token", err.Error())
	}
	publicIP := publicIPFrom(sockets)
	var ports [3]uint16
	for i, s := range sockets {
		ports[i] = s.ExternalPort
	}
	tok, err := probe.EncodeToken(probe.Token{IP: publicIP, Ports: ports, Session: session})
	if err != nil {
		return stepFail("probe token", err.Error())
	}
	display := probe.FormatToken(tok)
	fmt.Printf("\nProbe token: %s\n", display)
	fmt.Println("Send this to your peer. They run: punch probe <token>")
	fmt.Print("\nPeer's reply token: ")

	// Keep sockets alive while waiting.
	stopKeepalive := startKeepalive(sockets)
	reader := bufio.NewReader(os.Stdin)
	replyRaw, err := reader.ReadString('\n')
	close(stopKeepalive)
	if err != nil {
		return stepFail("reply token", "could not read reply: "+err.Error())
	}

	peerTok, err := probe.DecodeToken(strings.TrimSpace(replyRaw))
	if err != nil {
		return stepFail("reply token", err.Error())
	}
	if peerTok.Session != session {
		return stepFail("reply token", "session mismatch — make sure your peer used your probe token")
	}
	fmt.Fprintf(os.Stderr, "\n      → peer's external address: %s (ports: %d, %d, %d)\n",
		peerTok.IP, peerTok.Ports[0], peerTok.Ports[1], peerTok.Ports[2])

	return runProbeTest(sockets, peerTok)
}

// ── Responder ─────────────────────────────────────────────────────────────────

func runProbeResponder(rawTok string) error {
	initiatorTok, err := probe.DecodeToken(rawTok)
	if err != nil {
		return stepFail("probe token", "invalid token — "+err.Error())
	}
	fmt.Fprintf(os.Stderr, "      → initiator's external address: %s (ports: %d, %d, %d)\n",
		initiatorTok.IP, initiatorTok.Ports[0], initiatorTok.Ports[1], initiatorTok.Ports[2])

	sockets, err := bindAndShow()
	if err != nil {
		return err
	}
	defer probe.CloseAll(sockets)

	publicIP := publicIPFrom(sockets)
	var ports [3]uint16
	for i, s := range sockets {
		ports[i] = s.ExternalPort
	}
	replyTok, err := probe.EncodeToken(probe.Token{
		IP:      publicIP,
		Ports:   ports,
		Session: initiatorTok.Session, // echo session back
	})
	if err != nil {
		return stepFail("reply token", err.Error())
	}
	display := probe.FormatToken(replyTok)
	fmt.Printf("\nReply token: %s\n", display)
	fmt.Println("Send this back to your peer.")

	return runProbeTest(sockets, initiatorTok)
}

// ── Shared test logic ─────────────────────────────────────────────────────────

func runProbeTest(sockets []probe.Socket, peerTok probe.Token) error {
	peerIP := net.ParseIP(peerTok.IP)
	if peerIP == nil {
		return stepFail("probe", "invalid peer IP in token")
	}

	fmt.Fprintf(os.Stderr, "\n[   ] probing %d ports simultaneously (%ds)...\n",
		len(sockets), int(probe.ProbeTimeout.Seconds()))

	results := probe.Probe(sockets, peerIP, peerTok.Ports[:])

	fmt.Fprintln(os.Stderr)
	anyOK := false
	defaultOK := false
	var workingPorts []int

	for i, r := range results {
		if r.OK {
			anyOK = true
			if i == 0 {
				defaultOK = true
			}
			workingPorts = append(workingPorts, int(r.Local))
			fmt.Fprintf(os.Stderr, "  [ ✓ ] %-22s %s\n", r.Label, fmtRTT(r.RTT))
		} else {
			fmt.Fprintf(os.Stderr, "  [ ✗ ] %-22s timeout\n", r.Label)
		}
	}

	fmt.Fprintln(os.Stderr)

	if !anyOK {
		fmt.Fprintln(os.Stderr, "[ ✗ ] direct connection not possible")
		fmt.Fprintln(os.Stderr, "      → your ISP is blocking residential-to-residential UDP")
		fmt.Fprintln(os.Stderr, "      → switch to a mobile hotspot and run punch probe again")
		return fmt.Errorf("no ports reachable")
	}

	stepOK("probe", "direct connection possible")

	if defaultOK {
		fmt.Fprintln(os.Stderr, "      → default port works — punch share/join will connect as-is")
	} else {
		fmt.Fprintf(os.Stderr, "      → default port blocked, but port %d works\n", workingPorts[0])
		fmt.Fprintf(os.Stderr, "      → use: punch share --port %d\n", workingPorts[0])
		fmt.Fprintf(os.Stderr, "             punch join  --port %d <token>\n", workingPorts[0])
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func bindAndShow() ([]probe.Socket, error) {
	fmt.Fprintf(os.Stderr, "[   ] binding test sockets\n")
	sockets, err := probe.BindSockets()
	if err != nil {
		return nil, stepFail("bind", err.Error())
	}
	for _, s := range sockets {
		if s.Conn != nil {
			fmt.Fprintf(os.Stderr, "      → %-22s external port: %d\n", s.Label, s.ExternalPort)
		} else {
			fmt.Fprintf(os.Stderr, "      → %-22s skipped (port in use)\n", s.Label)
		}
	}
	stepOK("bind", "")
	return sockets, nil
}

func publicIPFrom(sockets []probe.Socket) string {
	for _, s := range sockets {
		if s.Conn != nil && s.ExternalIP != "" {
			return s.ExternalIP
		}
	}
	return ""
}

// startKeepalive sends periodic STUN discovers on each socket to keep NAT
// mappings alive while waiting for the peer's reply token.
func startKeepalive(sockets []probe.Socket) chan struct{} {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				for _, s := range sockets {
					if s.Conn != nil {
						// tiny packet to STUN server to refresh NAT mapping
						s.Conn.SetWriteDeadline(time.Now().Add(time.Second)) //nolint:errcheck
						s.Conn.WriteToUDP([]byte{0}, &net.UDPAddr{           //nolint:errcheck
							IP:   net.ParseIP("74.125.250.129"), // stun.l.google.com
							Port: 19302,
						})
					}
				}
			case <-done:
				return
			}
		}
	}()
	return done
}

func fmtRTT(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
