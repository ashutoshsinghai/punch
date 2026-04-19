// Package punch implements UDP NAT hole punching.
//
// How it works:
//
//  1. Alice (share side) binds a UDP socket on a random port and publishes
//     her public IP:port in the token.
//
//  2. Bob (join side) decodes the token to get Alice's public IP:port.
//     Bob sends a stream of UDP probe packets to Alice's address.
//     This creates a "hole" in Bob's NAT: traffic FROM Alice's IP:port will
//     now be let through.
//
//  3. Alice, meanwhile, is listening. When she receives Bob's first probe
//     she learns Bob's public IP:port. She replies immediately.
//     This creates a hole in Alice's NAT for Bob's IP:port.
//
//  4. Both holes are now open — packets flow both ways.
//
// This approach (simultaneous open via probe-listen) covers:
//   - Full Cone NAT: always works
//   - Address-Restricted NAT: works once Alice receives and replies to Bob's probe
//   - Port-Restricted NAT: works with simultaneous probing
//   - Symmetric NAT: does NOT work (reported clearly to the user)
package punch

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/ashutoshsinghai/punch/internal/transport"
)

const (
	probeInterval = 200 * time.Millisecond
	probeTimeout  = 2 * time.Minute
	probeMsg      = "PUNCH"
)

// Result holds the established connection after a successful hole punch.
type Result struct {
	Conn   *transport.Conn
	Local  *net.UDPAddr
	Remote *net.UDPAddr
}

// Listen binds a UDP socket on the given port and waits for a peer to punch through.
// This is the Alice (share) side.
func Listen(localPort uint16) (*Result, error) {
	addr := &net.UDPAddr{Port: int(localPort)}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind UDP port %d: %w", localPort, err)
	}

	conn.SetReadDeadline(time.Now().Add(probeTimeout))

	buf := make([]byte, 512)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("timed out waiting for peer (no connection within %s)", probeTimeout)
		}

		// Accept any probe from any peer (first come, first served).
		if n >= len(probeMsg) && string(buf[:len(probeMsg)]) == probeMsg {
			// Clear the read deadline before handing off.
			conn.SetReadDeadline(time.Time{})

			// Send a probe back to open our side of the NAT.
			conn.WriteToUDP([]byte(probeMsg), remote) //nolint:errcheck

			local := conn.LocalAddr().(*net.UDPAddr)
			tc := transport.Wrap(conn, remote)
			return &Result{Conn: tc, Local: local, Remote: remote}, nil
		}
	}
}

// Dial punches through to a peer. It tries the local (LAN) IP first to avoid
// hairpin NAT issues when both peers are on the same network, then falls back
// to the public IP for cross-network connections.
func Dial(remoteIP string, remotePort uint16) (*Result, error) {
	fmt.Fprintf(os.Stderr, "Connecting to %s:%d...\n", remoteIP, remotePort)
	result, err := dialOne(remoteIP, remotePort)
	if err != nil {
		return nil, fmt.Errorf(
			"direct connection failed — symmetric NAT detected or peer unreachable.\n"+
				"(error: %w)", err,
		)
	}
	return result, nil
}

// dialOne attempts hole punching to a single IP:port.
func dialOne(remoteIP string, remotePort uint16) (*Result, error) {
	remote := &net.UDPAddr{
		IP:   net.ParseIP(remoteIP),
		Port: int(remotePort),
	}
	if remote.IP == nil {
		return nil, fmt.Errorf("invalid remote IP: %s", remoteIP)
	}

	local := &net.UDPAddr{Port: 0}
	conn, err := net.ListenUDP("udp4", local)
	if err != nil {
		return nil, fmt.Errorf("failed to bind local UDP socket: %w", err)
	}

	deadline := time.Now().Add(probeTimeout)
	probeTicker := time.NewTicker(probeInterval)
	defer probeTicker.Stop()

	probe := []byte(probeMsg)
	buf := make([]byte, 512)

	for time.Now().Before(deadline) {
		select {
		case <-probeTicker.C:
			conn.WriteToUDP(probe, remote) //nolint:errcheck
		default:
		}

		conn.SetReadDeadline(time.Now().Add(probeInterval))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		if from.String() == remote.String() &&
			n >= len(probeMsg) && string(buf[:len(probeMsg)]) == probeMsg {
			conn.SetReadDeadline(time.Time{})
			localAddr := conn.LocalAddr().(*net.UDPAddr)
			tc := transport.Wrap(conn, remote)
			return &Result{Conn: tc, Local: localAddr, Remote: remote}, nil
		}
	}

	conn.Close()
	return nil, fmt.Errorf("no response from %s:%d within %s", remoteIP, remotePort, probeTimeout)
}

// SoloPunch performs simultaneous hole punching on conn to reach remote.
// Unlike Simultaneous, it does NOT wrap the connection in transport.Conn —
// the caller gets the raw *net.UDPConn back for use with a custom protocol
// (e.g. the sliding-window file transfer).
//
// Both sides must call SoloPunch at roughly the same time. The function
// returns once a probe is received from remote, indicating the hole is open.
func SoloPunch(conn *net.UDPConn, remote *net.UDPAddr) error {
	const timeout = 2 * time.Minute

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	probe := []byte(probeMsg)
	buf := make([]byte, 512)

	for time.Now().Before(deadline) {
		select {
		case <-ticker.C:
			conn.WriteToUDP(probe, remote) //nolint:errcheck
		default:
		}

		conn.SetReadDeadline(time.Now().Add(probeInterval))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		if from.String() == remote.String() &&
			n >= len(probeMsg) && string(buf[:len(probeMsg)]) == probeMsg {
			conn.SetReadDeadline(time.Time{})
			// Keep probing for 30 s so the peer can exit its own SoloPunch loop
			// even after the caller has moved on to QUIC dial/listen.  Without
			// this the peer only has ~2 s to receive a PUNCH packet before the
			// goroutine stops and only QUIC packets arrive — which SoloPunch
			// discards — leaving the peer stuck until timeout.
			go func() {
				deadline := time.Now().Add(30 * time.Second)
				for time.Now().Before(deadline) {
					conn.WriteToUDP(probe, remote) //nolint:errcheck
					time.Sleep(probeInterval)
				}
			}()
			return nil
		}
	}

	return fmt.Errorf(
		"file-transfer hole punch failed — symmetric NAT or peer unreachable (tried for %s)", timeout,
	)
}

// RandomPort picks a random available UDP port in the high range.
func RandomPort() (uint16, error) {
	addr := &net.UDPAddr{Port: 0}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return 0, err
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return uint16(port), nil
}

// ListenRaw is like Listen but returns the raw *net.UDPConn without wrapping
// it in transport.Conn. Use this when handing the socket to a library that
// manages its own reliability layer (e.g. QUIC).
func ListenRaw(localPort uint16) (*net.UDPConn, *net.UDPAddr, error) {
	addr := &net.UDPAddr{Port: int(localPort)}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to bind UDP port %d: %w", localPort, err)
	}
	conn.SetReadDeadline(time.Now().Add(probeTimeout))
	buf := make([]byte, 512)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("timed out waiting for peer (no connection within %s)", probeTimeout)
		}
		if n >= len(probeMsg) && string(buf[:len(probeMsg)]) == probeMsg {
			conn.SetReadDeadline(time.Time{})
			conn.WriteToUDP([]byte(probeMsg), remote) //nolint:errcheck
			return conn, remote, nil
		}
	}
}

// DialRaw is like Dial but returns the raw *net.UDPConn without wrapping it
// in transport.Conn. Use this when handing the socket to a library that
// manages its own reliability layer (e.g. QUIC).
func DialRaw(remoteIP string, remotePort uint16) (*net.UDPConn, *net.UDPAddr, error) {
	remote := &net.UDPAddr{
		IP:   net.ParseIP(remoteIP),
		Port: int(remotePort),
	}
	if remote.IP == nil {
		return nil, nil, fmt.Errorf("invalid remote IP: %s", remoteIP)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to bind local UDP socket: %w", err)
	}
	deadline := time.Now().Add(probeTimeout)
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	probe := []byte(probeMsg)
	buf := make([]byte, 512)
	for time.Now().Before(deadline) {
		select {
		case <-ticker.C:
			conn.WriteToUDP(probe, remote) //nolint:errcheck
		default:
		}
		conn.SetReadDeadline(time.Now().Add(probeInterval))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if from.String() == remote.String() && n >= len(probeMsg) && string(buf[:len(probeMsg)]) == probeMsg {
			conn.SetReadDeadline(time.Time{})
			return conn, remote, nil
		}
	}
	conn.Close()
	return nil, nil, fmt.Errorf("no response from %s:%d within %s", remoteIP, remotePort, probeTimeout)
}

// BindSocket creates a UDP socket on the given port and returns it ready
// for use (STUN discovery, hole punching, etc.).
// Pass port 0 to let the OS pick a random available port.
func BindSocket(port int) (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("failed to bind UDP socket on port %d: %w", port, err)
	}
	return conn, nil
}

// isPrivateIP returns true for RFC-1918 / link-local addresses (LAN peers).
func isPrivateIP(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil {
		return false
	}
	switch {
	case ip[0] == 10:
		return true
	case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31:
		return true
	case ip[0] == 192 && ip[1] == 168:
		return true
	case ip[0] == 169 && ip[1] == 254:
		return true
	}
	return false
}

// PunchDiag holds counters collected during a failed Simultaneous attempt.
type PunchDiag struct {
	TotalReceived int    // UDP packets received from any source
	WrongSource   int    // packets with correct PUNCH payload but wrong source IP:port
	WrongPayload  int    // packets from correct source but wrong payload
	WrongSourceIP string // last seen source when WrongSource > 0
}

// Simultaneous performs true simultaneous UDP hole punching.
// Both peers must know each other's public IP:port in advance (via token
// exchange) and call Simultaneous at roughly the same time.
//
// status is called every 5 seconds with a progress message while probing;
// pass nil to suppress progress output.
//
// If both peers are on the same LAN (same public IP / NAT hairpin not
// supported), it falls back to a LAN broadcast probe automatically —
// no token format change required.
func Simultaneous(conn *net.UDPConn, remote *net.UDPAddr, status func(string)) (*Result, *PunchDiag, error) {
	const timeout = 45 * time.Second

	// Enable broadcast so we can also probe 255.255.255.255:remote.Port
	// for peers behind the same NAT.
	enableBroadcast(conn)
	bcast := &net.UDPAddr{IP: net.IPv4bcast, Port: remote.Port}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	probe := []byte(probeMsg)
	buf := make([]byte, 512)

	start := time.Now()
	lastStatus := time.Time{} // zero → fire immediately on first tick

	diag := &PunchDiag{}

	for time.Now().Before(deadline) {
		select {
		case <-ticker.C:
			conn.WriteToUDP(probe, remote) //nolint:errcheck
			conn.WriteToUDP(probe, bcast)  //nolint:errcheck — LAN fallback
		default:
		}

		if status != nil && time.Since(lastStatus) >= 5*time.Second {
			elapsed := int(time.Since(start).Seconds())
			if elapsed == 0 {
				status("probing peer...")
			} else {
				status(fmt.Sprintf("probing peer... (%ds)", elapsed))
			}
			lastStatus = time.Now()
		}

		conn.SetReadDeadline(time.Now().Add(probeInterval))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		diag.TotalReceived++
		isPunch := n >= len(probeMsg) && string(buf[:len(probeMsg)]) == probeMsg
		isExpected := from.String() == remote.String() || isPrivateIP(from.IP)

		if isPunch && !isExpected {
			diag.WrongSource++
			diag.WrongSourceIP = from.String()
			continue
		}
		if !isPunch && isExpected {
			diag.WrongPayload++
			continue
		}
		if !isPunch || !isExpected {
			continue
		}

		conn.SetReadDeadline(time.Time{})
		local := conn.LocalAddr().(*net.UDPAddr)
		tc := transport.Wrap(conn, from) // use actual source (may be LAN IP)
		go func() {
			for i := 0; i < 20; i++ {
				conn.WriteToUDP(probe, from) //nolint:errcheck
				time.Sleep(probeInterval)
			}
		}()
		return &Result{Conn: tc, Local: local, Remote: from}, diag, nil
	}

	conn.Close()
	return nil, diag, fmt.Errorf("hole punch timed out after %s", timeout)
}
