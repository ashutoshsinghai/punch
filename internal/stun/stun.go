// Package stun implements a minimal STUN Binding Request (RFC 5389).
//
// It sends a single binding request from an existing UDP socket and
// parses the XOR-MAPPED-ADDRESS from the response. This tells the
// caller what public IP:port their NAT assigned to that socket —
// which is the address a remote peer must connect to.
//
// No new dependencies: pure standard library.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// Server is the default public STUN server used for discovery.
const Server = "stun.l.google.com:19302"

// Server2 is a second STUN server with a different IP, used for symmetric
// NAT detection. Querying two different destination IPs from the same local
// socket and comparing the resulting mapped ports reveals whether the NAT
// is symmetric (different port per destination) or not.
const Server2 = "stun3.l.google.com:19302"

// NATDiag holds the result of a two-server NAT diagnostic.
type NATDiag struct {
	PublicIP    string
	PublicPort  uint16 // mapped port as seen by server 1
	PublicPort2 uint16 // mapped port as seen by server 2 (0 if second query failed)
	IsCGNAT     bool   // public IP is in RFC 6598 (100.64.0.0/10) — ISP-level NAT
	IsSymmetric bool   // different mapped port observed per destination
}

// CheckNAT performs two STUN queries from conn to two different servers and
// returns a NAT diagnostic. It replaces a plain Discover call: the returned
// PublicIP/PublicPort are what should go into the token.
//
// Failure of the second STUN query is non-fatal — IsSymmetric is left false
// so the caller doesn't raise a false alarm.
func CheckNAT(conn *net.UDPConn) (*NATDiag, error) {
	ip1, port1, err := DiscoverFrom(conn, Server)
	if err != nil {
		return nil, err
	}

	diag := &NATDiag{
		PublicIP:   ip1,
		PublicPort: port1,
		IsCGNAT:    isCGNATorPrivate(ip1),
	}

	// Second query — different destination IP reveals symmetric NAT.
	_, port2, err := DiscoverFrom(conn, Server2)
	if err == nil {
		diag.PublicPort2 = port2
		diag.IsSymmetric = port1 != port2
	}

	return diag, nil
}

// isCGNATorPrivate returns true if ip is in a range that indicates the STUN
// server is seeing an ISP NAT address rather than a true public IP.
func isCGNATorPrivate(ipStr string) bool {
	ip := net.ParseIP(ipStr).To4()
	if ip == nil {
		return false
	}
	switch {
	case ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127: // RFC 6598 CGNAT
		return true
	case ip[0] == 10: // RFC 1918
		return true
	case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31: // RFC 1918
		return true
	case ip[0] == 192 && ip[1] == 168: // RFC 1918
		return true
	}
	return false
}

const (
	msgBindingRequest  = 0x0001
	msgBindingResponse = 0x0101
	magicCookie        = 0x2112A442

	attrXORMappedAddr = 0x0020
	attrMappedAddr    = 0x0001
)

// Discover sends a STUN Binding Request from conn to the default server and
// returns the public IP and port as observed by the STUN server.
//
// conn must already be bound (e.g. via net.ListenUDP). It is NOT
// closed — the caller continues to use it for hole punching.
func Discover(conn *net.UDPConn) (ip string, port uint16, err error) {
	return DiscoverFrom(conn, Server)
}

// DiscoverFrom sends a STUN Binding Request from conn to the given server
// (host:port) and returns the public IP and port the server observed.
func DiscoverFrom(conn *net.UDPConn, server string) (ip string, port uint16, err error) {
	serverAddr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		return "", 0, fmt.Errorf("resolve STUN server %s: %w", server, err)
	}

	// Build a 20-byte STUN Binding Request header (no attributes).
	var txID [12]byte
	if _, err := rand.Read(txID[:]); err != nil {
		return "", 0, fmt.Errorf("generate transaction ID: %w", err)
	}

	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:2], msgBindingRequest)
	binary.BigEndian.PutUint16(req[2:4], 0) // message length (no attributes)
	binary.BigEndian.PutUint32(req[4:8], magicCookie)
	copy(req[8:20], txID[:])

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetDeadline(time.Time{})

	if _, err := conn.WriteToUDP(req, serverAddr); err != nil {
		return "", 0, fmt.Errorf("send STUN request: %w", err)
	}

	buf := make([]byte, 512)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return "", 0, fmt.Errorf("read STUN response: %w", err)
		}
		// Ignore anything not from the STUN server (e.g. stray probes).
		if from.String() != serverAddr.String() {
			continue
		}
		return parseResponse(buf[:n], txID)
	}
}

func parseResponse(data []byte, txID [12]byte) (string, uint16, error) {
	if len(data) < 20 {
		return "", 0, fmt.Errorf("response too short (%d bytes)", len(data))
	}
	if binary.BigEndian.Uint16(data[0:2]) != msgBindingResponse {
		return "", 0, fmt.Errorf("unexpected message type 0x%04x", binary.BigEndian.Uint16(data[0:2]))
	}

	var gotTxID [12]byte
	copy(gotTxID[:], data[8:20])
	if gotTxID != txID {
		return "", 0, fmt.Errorf("transaction ID mismatch")
	}

	msgLen := int(binary.BigEndian.Uint16(data[2:4]))
	if 20+msgLen > len(data) {
		return "", 0, fmt.Errorf("truncated response")
	}
	attrs := data[20 : 20+msgLen]

	var fallbackIP string
	var fallbackPort uint16

	for len(attrs) >= 4 {
		attrType := binary.BigEndian.Uint16(attrs[0:2])
		attrLen := int(binary.BigEndian.Uint16(attrs[2:4]))
		if 4+attrLen > len(attrs) {
			break
		}
		val := attrs[4 : 4+attrLen]

		switch attrType {
		case attrXORMappedAddr:
			// val: [0]=reserved [1]=family [2:4]=XOR'd port [4:8]=XOR'd IP
			if len(val) >= 8 && val[1] == 0x01 { // IPv4
				p := binary.BigEndian.Uint16(val[2:4]) ^ uint16(magicCookie>>16)
				cookie := [4]byte{0x21, 0x12, 0xA4, 0x42}
				ip := make(net.IP, 4)
				for i := range ip {
					ip[i] = val[4+i] ^ cookie[i]
				}
				return ip.String(), p, nil // prefer XOR-MAPPED-ADDRESS
			}
		case attrMappedAddr:
			if len(val) >= 8 && val[1] == 0x01 {
				fallbackPort = binary.BigEndian.Uint16(val[2:4])
				fallbackIP = net.IP(val[4:8]).String()
			}
		}

		// Attributes are padded to 4-byte boundaries.
		padded := (attrLen + 3) &^ 3
		attrs = attrs[4+padded:]
	}

	if fallbackIP != "" {
		return fallbackIP, fallbackPort, nil
	}
	return "", 0, fmt.Errorf("no mapped address in STUN response")
}
