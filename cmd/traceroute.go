package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

var tracerouteCmd = &cobra.Command{
	Use:   "traceroute <token>",
	Short: "Trace the UDP path to a peer and show where packets drop",
	Args:  cobra.ExactArgs(1),
	RunE:  runTraceroute,
}

func init() {
	rootCmd.AddCommand(tracerouteCmd)
}

func runTraceroute(_ *cobra.Command, args []string) error {
	payload, err := token.Decode(args[0])
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	destIP := net.ParseIP(payload.IP).To4()
	if destIP == nil {
		return fmt.Errorf("invalid peer IP: %s", payload.IP)
	}

	fmt.Fprintln(os.Stderr)

	// ── Peer info ─────────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "Peer: %s:%d\n", payload.IP, payload.Port)
	if loc := traceGeoLookup(payload.IP); loc != "" {
		fmt.Fprintf(os.Stderr, "      %s\n", loc)
	}
	fmt.Fprintln(os.Stderr)

	// ── ICMP socket ───────────────────────────────────────────────────────────
	icmpConn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		if isTracePermErr(err) && os.Getuid() != 0 {
			return reExecWithSudo()
		}
		return fmt.Errorf("ICMP listen: %w", err)
	}
	defer icmpConn.Close()

	// ── UDP socket for sending probes ─────────────────────────────────────────
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return fmt.Errorf("bind UDP: %w", err)
	}
	defer udpConn.Close()

	p4 := ipv4.NewPacketConn(udpConn)
	probe := []byte("punch-trace")

	// ── TTL-based path trace ──────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "Tracing UDP path to %s  (30 hops max, 1s timeout per hop)\n\n", payload.IP)

	reached := false
	finalHops := 0
	var finalRTT time.Duration

	for ttl := 1; ttl <= 30; ttl++ {
		if err := p4.SetTTL(ttl); err != nil {
			return fmt.Errorf("set TTL: %w", err)
		}

		dest := &net.UDPAddr{IP: destIP, Port: 33434 + ttl}
		start := time.Now()
		udpConn.WriteTo(probe, dest) //nolint:errcheck

		icmpConn.SetDeadline(time.Now().Add(time.Second)) //nolint:errcheck
		buf := make([]byte, 1500)
		n, from, err := icmpConn.ReadFrom(buf)
		rtt := time.Since(start)

		if err != nil {
			fmt.Fprintf(os.Stderr, "  %2d   *                    timeout\n", ttl)
			continue
		}

		msg, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %2d   *                    (parse error)\n", ttl)
			continue
		}

		hopIP := from.String()
		geo := traceGeoLookup(hopIP)
		rttStr := fmt.Sprintf("%.1fms", float64(rtt.Microseconds())/1000)

		switch msg.Type {
		case ipv4.ICMPTypeTimeExceeded:
			fmt.Fprintf(os.Stderr, "  %2d   %-18s  %7s   %s\n", ttl, hopIP, rttStr, geo)

		case ipv4.ICMPTypeDestinationUnreachable:
			fmt.Fprintf(os.Stderr, "  %2d   %-18s  %7s   %s  ← peer\n", ttl, hopIP, rttStr, geo)
			finalHops = ttl
			finalRTT = rtt
			reached = true
		}

		if reached {
			break
		}
	}

	fmt.Fprintln(os.Stderr)
	if reached {
		fmt.Fprintf(os.Stderr, "%d hops · %.0fms round-trip\n", finalHops, float64(finalRTT.Milliseconds()))
	} else {
		fmt.Fprintln(os.Stderr, "Peer not reached within 30 hops — most routers suppress ICMP TTL replies.")
	}

	// ── Port reachability probe ───────────────────────────────────────────────
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "Port reachability to %s:\n\n", payload.IP)

	testPorts := []struct {
		port int
		name string
	}{
		{3478, "STUN"},
		{53, "DNS"},
		{443, "QUIC/HTTPS"},
		{19302, "STUN-alt"},
		{12345, "random"},
	}

	if err := p4.SetTTL(64); err != nil {
		return fmt.Errorf("reset TTL: %w", err)
	}

	var workingPorts []int

	for _, tp := range testPorts {
		dest := &net.UDPAddr{IP: destIP, Port: tp.port}
		start := time.Now()
		udpConn.WriteTo(probe, dest) //nolint:errcheck

		icmpConn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
		buf := make([]byte, 1500)
		n, from, err := icmpConn.ReadFrom(buf)
		rtt := time.Since(start)

		gotReply := false
		if err == nil {
			msg, _ := icmp.ParseMessage(1, buf[:n])
			if msg != nil && from.String() == payload.IP {
				gotReply = true
			}
		}

		if gotReply {
			fmt.Fprintf(os.Stderr, "  %-5d  %-12s  ✓  %.0fms — packet reached peer\n",
				tp.port, tp.name, float64(rtt.Milliseconds()))
			workingPorts = append(workingPorts, tp.port)
		} else {
			fmt.Fprintf(os.Stderr, "  %-5d  %-12s  ✗  timeout — dropped somewhere\n",
				tp.port, tp.name)
		}
	}

	// ── Verdict ───────────────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr)
	if len(workingPorts) > 0 {
		portStrs := make([]string, len(workingPorts))
		for i, p := range workingPorts {
			portStrs[i] = fmt.Sprintf("%d", p)
		}
		fmt.Fprintf(os.Stderr, "Ports that reach peer: %s\n", strings.Join(portStrs, ", "))
		fmt.Fprintf(os.Stderr, "Suggested fix: punch share --port %d\n", workingPorts[0])
	} else {
		fmt.Fprintln(os.Stderr, "No ports reached peer — ISP is silently dropping all UDP to this IP.")
		fmt.Fprintln(os.Stderr, "Suggested fix: ask your peer to connect from a mobile hotspot.")
	}
	fmt.Fprintln(os.Stderr)

	return nil
}

// reExecWithSudo re-runs the current process under sudo.
// On Windows, prints a manual instruction instead.
func reExecWithSudo() error {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "traceroute requires elevated privileges.")
		fmt.Fprintln(os.Stderr, "Right-click your terminal → Run as Administrator, then retry.")
		return nil
	}
	fmt.Fprintln(os.Stderr, "traceroute requires raw socket access — re-running with sudo...")
	fmt.Fprintln(os.Stderr)
	cmd := exec.Command("sudo", os.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isTracePermErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "permission denied") || strings.Contains(s, "operation not permitted")
}

// traceGeoLookup returns a one-line "City, Region, CC — ISP" string for an IP.
// Returns "private network" for RFC-1918 addresses, empty string on failure.
func traceGeoLookup(ipStr string) string {
	parsed := net.ParseIP(ipStr)
	if parsed == nil {
		return ""
	}
	if parsed.IsLoopback() || parsed.IsPrivate() {
		return "private network"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://ip-api.com/json/" + ipStr +
		"?fields=status,message,country,countryCode,regionName,city,isp,org")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var result struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
		RegionName  string `json:"regionName"`
		City        string `json:"city"`
		ISP         string `json:"isp"`
		Org         string `json:"org"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Status != "success" {
		return ""
	}

	provider := result.ISP
	if result.Org != "" && result.Org != result.ISP {
		provider = result.Org
	}
	return fmt.Sprintf("%s, %s, %s — %s", result.City, result.RegionName, result.CountryCode, provider)
}
