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

// BindSocket creates a new UDP socket on a random available port and
// returns it ready for use (STUN discovery, hole punching, etc.).
func BindSocket() (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, fmt.Errorf("failed to bind UDP socket: %w", err)
	}
	return conn, nil
}

// Simultaneous performs true simultaneous UDP hole punching.
// Both peers must know each other's public IP:port in advance (via token
// exchange) and call Simultaneous at roughly the same time.
//
// Bob should call this immediately after printing his reply token so that
// his probes keep the NAT hole open while Alice reads the token.
// Alice calls it after entering Bob's reply token.
// Once both sides are probing each other the holes open and they connect.
func Simultaneous(conn *net.UDPConn, remote *net.UDPAddr) (*Result, error) {
	const timeout = 10 * time.Minute

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
			local := conn.LocalAddr().(*net.UDPAddr)
			tc := transport.Wrap(conn, remote)
			// Keep probing for a short window so the peer can also exit
			// their Simultaneous loop — they may not have received our
			// probe yet when we return here.
			go func() {
				for i := 0; i < 20; i++ {
					conn.WriteToUDP(probe, remote) //nolint:errcheck
					time.Sleep(probeInterval)
				}
			}()
			return &Result{Conn: tc, Local: local, Remote: remote}, nil
		}
	}

	conn.Close()
	return nil, fmt.Errorf(
		"direct connection failed — symmetric NAT detected or peer unreachable.\n" +
			"Ask your peer to try from a different network.\n" +
			"(tried for %s)", timeout,
	)
}
