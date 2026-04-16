package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const keySize = 32 // 256-bit key for ChaCha20-Poly1305

// DeriveKey derives a 32-byte symmetric key from the session ID using HKDF-SHA256.
// Both peers run this independently and arrive at the same key.
func DeriveKey(session string) ([]byte, error) {
	r := hkdf.New(sha256.New, []byte(session), []byte("punch-v1"), []byte("chacha20-key"))
	key := make([]byte, keySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}
	return key, nil
}

// Cipher holds the AEAD cipher for a session.
type Cipher struct {
	aead interface {
		Overhead() int
		NonceSize() int
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	}
}

// NewCipher creates a ChaCha20-Poly1305 cipher from a session ID.
func NewCipher(session string) (*Cipher, error) {
	key, err := DeriveKey(session)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("cipher init failed: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt encrypts plaintext and returns [nonce || ciphertext].
// A fresh random nonce is generated for every call.
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce generation failed: %w", err)
	}

	ciphertext := c.aead.Seal(nil, nonce, plaintext, nil)

	// Prepend nonce so the receiver can split it off.
	out := make([]byte, len(nonce)+len(ciphertext))
	copy(out, nonce)
	copy(out[len(nonce):], ciphertext)
	return out, nil
}

// Decrypt decrypts [nonce || ciphertext] and returns the plaintext.
func (c *Cipher) Decrypt(data []byte) ([]byte, error) {
	nonceSize := c.aead.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce := data[:nonceSize]
	ciphertext := data[nonceSize:]

	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong key or corrupted data): %w", err)
	}
	return plaintext, nil
}

// Frame wraps an encrypted payload with a 4-byte length prefix for framing over UDP.
// Layout: [4-byte big-endian length][encrypted payload]
func Frame(encrypted []byte) []byte {
	buf := make([]byte, 4+len(encrypted))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
	copy(buf[4:], encrypted)
	return buf
}

// Unframe extracts the payload from a framed buffer.
func Unframe(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("frame too short")
	}
	length := int(binary.BigEndian.Uint32(data[:4]))
	if len(data) < 4+length {
		return nil, fmt.Errorf("frame truncated: expected %d bytes, got %d", length, len(data)-4)
	}
	return data[4 : 4+length], nil
}
