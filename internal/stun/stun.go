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

const (
	msgBindingRequest  = 0x0001
	msgBindingResponse = 0x0101
	magicCookie        = 0x2112A442

	attrXORMappedAddr = 0x0020
	attrMappedAddr    = 0x0001
)

// Discover sends a STUN Binding Request from conn and returns the
// public IP and port as observed by the STUN server.
//
// conn must already be bound (e.g. via net.ListenUDP). It is NOT
// closed — the caller continues to use it for hole punching.
func Discover(conn *net.UDPConn) (ip string, port uint16, err error) {
	serverAddr, err := net.ResolveUDPAddr("udp4", Server)
	if err != nil {
		return "", 0, fmt.Errorf("resolve STUN server: %w", err)
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
