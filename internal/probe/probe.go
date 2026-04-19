// Package probe implements punch probe — simultaneous bidirectional UDP port
// reachability testing between two peers.
//
// Unlike nmap (one-directional), both sides probe each other at the same time,
// which matches the actual hole-punching behaviour of punch share/join.
// This tells you which ports actually work end-to-end and what --port flag to use.
package probe

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/ashutoshsinghai/punch/internal/stun"
)

const (
	probeMsg      = "PROBE"
	ProbeTimeout  = 10 * time.Second
	probeInterval = 200 * time.Millisecond
)

// testDefs are the local ports we bind and test.
// 0 means random — simulates default punch share/join behaviour.
var testDefs = []struct {
	local int
	label string
}{
	{0, "random (default)"},
	{3478, "3478  (STUN)"},
	{19302, "19302 (STUN-alt)"},
}

// Socket is a bound UDP socket with its STUN-discovered external address.
type Socket struct {
	Conn         *net.UDPConn
	ExternalIP   string
	ExternalPort uint16
	Label        string
}

// Result is the outcome of probing one port pair.
type Result struct {
	Label string
	Local uint16 // our external port
	Peer  uint16 // peer's external port
	OK    bool
	RTT   time.Duration
}

// BindSockets binds sockets on the test ports and discovers their external
// address via STUN.  Ports that are already in use are skipped gracefully.
func BindSockets() ([]Socket, error) {
	var sockets []Socket
	for _, td := range testDefs {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: td.local})
		if err != nil {
			// Port already in use — skip, but keep slot so indices align.
			sockets = append(sockets, Socket{Label: td.label})
			continue
		}
		ip, port, err := stun.Discover(conn)
		if err != nil {
			conn.Close()
			sockets = append(sockets, Socket{Label: td.label})
			continue
		}
		sockets = append(sockets, Socket{
			Conn:         conn,
			ExternalIP:   ip,
			ExternalPort: port,
			Label:        td.label,
		})
	}
	// Need at least one working socket.
	any := false
	for _, s := range sockets {
		if s.Conn != nil {
			any = true
			break
		}
	}
	if !any {
		return nil, fmt.Errorf("could not bind any test socket (STUN unreachable?)")
	}
	return sockets, nil
}

// CloseAll closes all sockets in the slice.
func CloseAll(sockets []Socket) {
	for _, s := range sockets {
		if s.Conn != nil {
			s.Conn.Close()
		}
	}
}

// Probe simultaneously sends PROBE packets from each of our sockets to the
// peer's corresponding external port.  Returns one Result per test slot.
func Probe(sockets []Socket, peerIP net.IP, peerPorts []uint16) []Result {
	results := make([]Result, len(sockets))
	var wg sync.WaitGroup

	for i, s := range sockets {
		if s.Conn == nil || i >= len(peerPorts) || peerPorts[i] == 0 {
			results[i] = Result{Label: s.Label, OK: false}
			continue
		}
		wg.Add(1)
		go func(idx int, sock Socket, peerPort uint16) {
			defer wg.Done()
			results[idx] = probeOne(sock, peerIP, peerPort)
		}(i, s, peerPorts[i])
	}

	wg.Wait()
	return results
}

func probeOne(sock Socket, peerIP net.IP, peerPort uint16) Result {
	remote := &net.UDPAddr{IP: peerIP, Port: int(peerPort)}
	probe := []byte(probeMsg)
	buf := make([]byte, 64)
	deadline := time.Now().Add(ProbeTimeout)
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	start := time.Now()
	for time.Now().Before(deadline) {
		select {
		case <-ticker.C:
			sock.Conn.WriteToUDP(probe, remote) //nolint:errcheck
		default:
		}
		sock.Conn.SetReadDeadline(time.Now().Add(probeInterval)) //nolint:errcheck
		n, from, err := sock.Conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if from.IP.Equal(peerIP) && n >= len(probeMsg) && string(buf[:len(probeMsg)]) == probeMsg {
			// Send a few more so the peer's loop can also exit.
			go func() {
				for i := 0; i < 15; i++ {
					sock.Conn.WriteToUDP(probe, remote) //nolint:errcheck
					time.Sleep(probeInterval)
				}
			}()
			return Result{
				Label: sock.Label,
				Local: sock.ExternalPort,
				Peer:  peerPort,
				OK:    true,
				RTT:   time.Since(start),
			}
		}
	}
	return Result{Label: sock.Label, Local: sock.ExternalPort, Peer: peerPort, OK: false}
}

// ── Token encoding ────────────────────────────────────────────────────────────
// Wire format (12 bytes):
//
//	[4b IPv4] [2b port0] [2b port1] [2b port2] [2b session]
//
// Encoded as base58 (~17 chars), displayed with dashes every 4.

const tokenSize = 12

// Token carries the probe initiator's or responder's external addresses.
type Token struct {
	IP      string
	Ports   [3]uint16 // external ports for [random, 3478, 19302] sockets
	Session [2]byte
}

// NewSession generates a random 2-byte session identifier.
func NewSession() ([2]byte, error) {
	var s [2]byte
	_, err := rand.Read(s[:])
	return s, err
}

// EncodeToken serialises t to a base58 string.
func EncodeToken(t Token) (string, error) {
	ip := net.ParseIP(t.IP).To4()
	if ip == nil {
		return "", fmt.Errorf("invalid IPv4: %s", t.IP)
	}
	buf := make([]byte, tokenSize)
	copy(buf[0:4], ip)
	binary.BigEndian.PutUint16(buf[4:6], t.Ports[0])
	binary.BigEndian.PutUint16(buf[6:8], t.Ports[1])
	binary.BigEndian.PutUint16(buf[8:10], t.Ports[2])
	copy(buf[10:12], t.Session[:])
	return b58Enc(buf), nil
}

// DecodeToken parses a base58 probe token string.
func DecodeToken(tok string) (Token, error) {
	tok = strings.TrimSpace(tok)
	tok = strings.ReplaceAll(tok, "-", "")
	buf, err := b58Dec(tok)
	if err != nil {
		return Token{}, fmt.Errorf("invalid probe token: %w", err)
	}
	if len(buf) < tokenSize {
		return Token{}, fmt.Errorf("invalid probe token: too short")
	}
	var t Token
	t.IP = net.IP(buf[0:4]).String()
	t.Ports[0] = binary.BigEndian.Uint16(buf[4:6])
	t.Ports[1] = binary.BigEndian.Uint16(buf[6:8])
	t.Ports[2] = binary.BigEndian.Uint16(buf[8:10])
	copy(t.Session[:], buf[10:12])
	return t, nil
}

// FormatToken returns the token string split into dash-separated groups of 4.
func FormatToken(tok string) string {
	var parts []string
	for i := 0; i < len(tok); i += 4 {
		end := i + 4
		if end > len(tok) {
			end = len(tok)
		}
		parts = append(parts, tok[i:end])
	}
	return strings.Join(parts, "-")
}

// ── base58 (Bitcoin alphabet, same as token package) ─────────────────────────

const b58Alpha = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func b58Enc(input []byte) string {
	leading := 0
	for _, b := range input {
		if b != 0 {
			break
		}
		leading++
	}
	num := make([]byte, len(input))
	copy(num, input)
	var result []byte
	for len(num) > 0 {
		rem := 0
		var next []byte
		for _, b := range num {
			cur := rem*256 + int(b)
			q := cur / 58
			rem = cur % 58
			if len(next) > 0 || q != 0 {
				next = append(next, byte(q))
			}
		}
		result = append(result, b58Alpha[rem])
		num = next
	}
	for i := 0; i < leading; i++ {
		result = append(result, b58Alpha[0])
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return string(result)
}

func b58Dec(input string) ([]byte, error) {
	leading := 0
	for _, c := range input {
		if c != rune(b58Alpha[0]) {
			break
		}
		leading++
	}
	num := []int{0}
	for _, c := range input {
		idx := strings.IndexRune(b58Alpha, c)
		if idx < 0 {
			return nil, fmt.Errorf("invalid character %q in probe token", c)
		}
		carry := idx
		for i := len(num) - 1; i >= 0; i-- {
			carry += num[i] * 58
			num[i] = carry % 256
			carry /= 256
		}
		for carry > 0 {
			num = append([]int{carry % 256}, num...)
			carry /= 256
		}
	}
	result := make([]byte, leading)
	for _, v := range num {
		if len(result) > leading || v != 0 {
			result = append(result, byte(v))
		}
	}
	return result, nil
}
