// Package token encodes/decodes punch session tokens.
//
// Wire format (14 bytes, binary):
//
//	[4 bytes public IPv4] [2 bytes port] [4 bytes session] [4 bytes expiry uint32]
//
// 14 bytes → ~19 base58 chars → displayed as groups of 4: "xxxx-xxxx-xxxx-xxxx-xxx"
package token

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

// Payload is the data encoded in a token.
type Payload struct {
	IP      string  // public IP as discovered via STUN
	Port    uint16
	Session [4]byte // random session bytes, shared secret for key derivation
	Exp     uint32  // unix timestamp (seconds)
}

// DefaultExpiry is how long a token is valid.
const DefaultExpiry = 10 * time.Minute

// payloadSize is the fixed binary size of a serialised Payload.
const payloadSize = 4 + 2 + 4 + 4 // 14 bytes

// base58 alphabet (Bitcoin variant — excludes 0, O, I, l to avoid visual confusion).
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// Encode serialises payload → 14-byte binary → base58 string (~19 chars).
func Encode(p Payload) (string, error) {
	pubIP := net.ParseIP(p.IP).To4()
	if pubIP == nil {
		return "", fmt.Errorf("invalid public IPv4 address: %s", p.IP)
	}

	buf := make([]byte, payloadSize)
	copy(buf[0:4], pubIP)
	binary.BigEndian.PutUint16(buf[4:6], p.Port)
	copy(buf[6:10], p.Session[:])
	binary.BigEndian.PutUint32(buf[10:14], p.Exp)

	return base58Enc(buf), nil
}

// Decode parses a base58 token string back into a Payload.
// Returns an error if the token is malformed or expired.
func Decode(tok string) (Payload, error) {
	tok = strings.TrimSpace(tok)
	tok = strings.ReplaceAll(tok, "-", "") // strip display separators

	buf, err := base58Dec(tok)
	if err != nil {
		return Payload{}, fmt.Errorf("invalid token: %w", err)
	}

	if len(buf) < payloadSize {
		return Payload{}, fmt.Errorf("invalid token: too short (%d bytes, need %d)", len(buf), payloadSize)
	}

	var p Payload
	p.IP = net.IP(buf[0:4]).String()
	p.Port = binary.BigEndian.Uint16(buf[4:6])
	copy(p.Session[:], buf[6:10])
	p.Exp = binary.BigEndian.Uint32(buf[10:14])

	if uint32(time.Now().Unix()) > p.Exp {
		return Payload{}, fmt.Errorf("token expired")
	}

	return p, nil
}

// SessionHex returns the session bytes as a lowercase hex string (used for key derivation).
func (p Payload) SessionHex() string {
	return fmt.Sprintf("%x", p.Session)
}

// NewReplyPayload builds a reply token for the join side to send back to the
// share side. It reuses the original session so both peers derive the same
// encryption key.
func NewReplyPayload(publicIP string, port uint16, session [4]byte) (Payload, error) {
	pubIP := net.ParseIP(publicIP).To4()
	if pubIP == nil {
		return Payload{}, fmt.Errorf("invalid public IPv4 address: %s", publicIP)
	}
	return Payload{
		IP:      publicIP,
		Port:    port,
		Session: session,
		Exp:     uint32(time.Now().Add(DefaultExpiry).Unix()),
	}, nil
}

// NewPayload builds a Payload with a fresh random session and given expiry.
func NewPayload(publicIP string, port uint16, expiry time.Duration) (Payload, error) {
	var session [4]byte
	if _, err := rand.Read(session[:]); err != nil {
		return Payload{}, fmt.Errorf("failed to generate session: %w", err)
	}
	return Payload{
		IP:      publicIP,
		Port:    port,
		Session: session,
		Exp:     uint32(time.Now().Add(expiry).Unix()),
	}, nil
}

// Words formats the token in human-readable dash-separated groups of 4.
// The full token is preserved — do NOT change case (base58 is case-sensitive).
func Words(tok string) string {
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

// --- base58 encoding/decoding ---

func base58Enc(input []byte) string {
	leadingZeros := 0
	for _, b := range input {
		if b != 0 {
			break
		}
		leadingZeros++
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
		result = append(result, base58Alphabet[rem])
		num = next
	}

	for i := 0; i < leadingZeros; i++ {
		result = append(result, base58Alphabet[0])
	}

	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}

func base58Dec(input string) ([]byte, error) {
	leadingZeros := 0
	for _, c := range input {
		if c != rune(base58Alphabet[0]) {
			break
		}
		leadingZeros++
	}

	num := []int{0}
	for _, c := range input {
		charIndex := strings.IndexRune(base58Alphabet, c)
		if charIndex < 0 {
			return nil, fmt.Errorf("invalid character %q", c)
		}

		carry := charIndex
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

	result := make([]byte, leadingZeros)
	for _, v := range num {
		if len(result) > leadingZeros || v != 0 {
			result = append(result, byte(v))
		}
	}

	return result, nil
}
