// Package filetransfer implements a sliding-window (Go-Back-N) file transfer
// protocol over a raw UDP connection.
//
// Each chunk is encrypted independently using the provided Cipher before
// being written to the wire. The receiver verifies a SHA-256 hash of the
// complete file once it receives the EOF sentinel.
//
// Wire packet layout:
//
//	DATA:  [0x01][4-byte seq BE][encrypted chunk]
//	ACK:   [0x02][4-byte ack BE]          — cumulative: "received all ≤ ack"
//	EOF:   [0x03][4-byte zero][encrypted 32-byte SHA-256 hash]
//	ABORT: [0x04][4-byte zero]
package filetransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/ashutoshsinghai/punch/internal/crypto"
)

const (
	typeData  byte = 0x01
	typeACK   byte = 0x02
	typeEOF   byte = 0x03
	typeAbort byte = 0x04

	// ChunkSize is the plaintext payload per DATA packet.
	// Must fit inside a single UDP datagram after headers + encryption overhead:
	//   Ethernet MTU 1500 - IP(20) - UDP(8) - pkt header(5) - ChaCha20 overhead(28) = 1439
	// Use 1400 to leave headroom for PPPoE, VPN tunnels, etc.
	ChunkSize = 1400

	// WindowSize is the number of unacknowledged chunks in flight.
	WindowSize = 64

	ackTimeout    = 3 * time.Second  // retransmit window after no ACK
	transferDeadline = 90 * time.Second // max silence before giving up
)

// headerSize is the fixed 5-byte header (type + seq).
const headerSize = 5

// maxPacket is the largest packet we ever read: header + encrypted chunk.
// ChaCha20-Poly1305 overhead is 12 (nonce) + 16 (tag) = 28 bytes.
const maxPacket = headerSize + ChunkSize + 28

// Send streams the file at filePath to remote using a sliding window protocol.
// progress(sent, total) is called after each ACKed window advance; may be nil.
func Send(conn *net.UDPConn, remote *net.UDPAddr, filePath string, cipher *crypto.Cipher, progress func(int64, int64)) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()

	// First pass: compute SHA-256 hash and total size.
	hasher := sha256.New()
	totalSize, err := io.Copy(hasher, f)
	if err != nil {
		return fmt.Errorf("hashing %s: %w", filePath, err)
	}
	fileHash := hasher.Sum(nil)

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek: %w", err)
	}

	totalChunks := uint32((totalSize + ChunkSize - 1) / ChunkSize)
	if totalSize == 0 {
		totalChunks = 0
	}

	// Goroutine to drain incoming ACKs into a channel.
	ackCh := make(chan uint32, WindowSize*4)
	stopACKReader := make(chan struct{})
	go func() {
		buf := make([]byte, 64)
		for {
			select {
			case <-stopACKReader:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(transferDeadline))
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			if addr.String() != remote.String() {
				continue
			}
			if n < headerSize || buf[0] != typeACK {
				continue
			}
			seq := binary.BigEndian.Uint32(buf[1:5])
			select {
			case ackCh <- seq:
			default:
			}
		}
	}()
	defer close(stopACKReader)

	// Sliding window: Go-Back-N.
	windowBase := uint32(0)
	nextSeq := uint32(0)
	chunkBuf := make([]byte, ChunkSize)

	sendChunk := func(seq uint32) error {
		offset := int64(seq) * ChunkSize
		n, err := f.ReadAt(chunkBuf, offset)
		if err != nil && err != io.EOF {
			return fmt.Errorf("read chunk %d: %w", seq, err)
		}
		enc, err := cipher.Encrypt(chunkBuf[:n])
		if err != nil {
			return fmt.Errorf("encrypt chunk %d: %w", seq, err)
		}
		pkt := makePacket(typeData, seq, enc)
		_, err = conn.WriteToUDP(pkt, remote)
		return err
	}

	for windowBase < totalChunks {
		// Fill the window.
		for nextSeq < windowBase+WindowSize && nextSeq < totalChunks {
			if err := sendChunk(nextSeq); err != nil {
				return err
			}
			nextSeq++
		}

		// Wait for an ACK or a retransmit timeout.
		timer := time.NewTimer(ackTimeout)
		gotACK := false

	drainACKs:
		for {
			select {
			case ack := <-ackCh:
				timer.Stop()
				if ack+1 > windowBase {
					windowBase = ack + 1
					if progress != nil {
						sent := int64(windowBase) * ChunkSize
						if sent > totalSize {
							sent = totalSize
						}
						progress(sent, totalSize)
					}
				}
				gotACK = true
				// Drain any additional queued ACKs before re-filling window.
			drainMore:
				for {
					select {
					case more := <-ackCh:
						if more+1 > windowBase {
							windowBase = more + 1
							if progress != nil {
								sent := int64(windowBase) * ChunkSize
								if sent > totalSize {
									sent = totalSize
								}
								progress(sent, totalSize)
							}
						}
					default:
						break drainMore
					}
				}
				break drainACKs

			case <-timer.C:
				if !gotACK {
					// Go-Back-N: retransmit the whole window.
					nextSeq = windowBase
				}
				break drainACKs
			}
		}
	}

	// Send EOF with encrypted hash. Retry several times to survive packet loss.
	hashEnc, err := cipher.Encrypt(fileHash)
	if err != nil {
		return fmt.Errorf("encrypt hash: %w", err)
	}
	eofPkt := makePacket(typeEOF, 0, hashEnc)
	for i := 0; i < 15; i++ {
		conn.WriteToUDP(eofPkt, remote) //nolint:errcheck
		time.Sleep(200 * time.Millisecond)
	}

	if progress != nil {
		progress(totalSize, totalSize)
	}
	return nil
}

// Receive reads chunks from remote, writes them to savePath, and verifies
// the SHA-256 hash sent in the EOF packet.
// progress(received, total) is called after each in-order chunk; may be nil.
func Receive(conn *net.UDPConn, remote *net.UDPAddr, savePath string, totalSize int64, cipher *crypto.Cipher, progress func(int64, int64)) error {
	partPath := savePath + ".part"
	f, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", partPath, err)
	}
	closed := false
	defer func() {
		if !closed {
			f.Close()
			os.Remove(partPath)
		}
	}()

	expected := uint32(0)
	received := int64(0)
	buf := make([]byte, maxPacket)

	sendACK := func(seq uint32) {
		pkt := makePacket(typeACK, seq, nil)
		conn.WriteToUDP(pkt, remote) //nolint:errcheck
	}

	for {
		conn.SetReadDeadline(time.Now().Add(transferDeadline))
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return fmt.Errorf("receive timeout (no data for %s)", transferDeadline)
		}
		if addr.String() != remote.String() {
			continue
		}
		if n < headerSize {
			continue
		}

		pktType := buf[0]
		seq := binary.BigEndian.Uint32(buf[1:5])
		payload := buf[headerSize:n]

		switch pktType {
		case typeData:
			if seq == expected {
				plain, err := cipher.Decrypt(payload)
				if err != nil {
					// Decryption failure on an in-order chunk is fatal.
					return fmt.Errorf("decrypt chunk %d: %w", seq, err)
				}
				if _, err := f.Write(plain); err != nil {
					return fmt.Errorf("write chunk %d: %w", seq, err)
				}
				received += int64(len(plain))
				sendACK(seq)
				expected++
				if progress != nil {
					progress(received, totalSize)
				}
			} else if seq < expected {
				// Duplicate — re-ACK so sender can slide its window.
				sendACK(seq)
			} else {
				// Out-of-order (Go-Back-N): NAK by re-ACKing last in-order.
				if expected > 0 {
					sendACK(expected - 1)
				}
			}

		case typeEOF:
			// Verify hash.
			if len(payload) == 0 {
				return fmt.Errorf("EOF packet missing hash")
			}
			hashBytes, err := cipher.Decrypt(payload)
			if err != nil {
				return fmt.Errorf("decrypt EOF hash: %w", err)
			}

			f.Close()
			closed = true

			got, err := fileHash(partPath)
			if err != nil {
				return fmt.Errorf("hash verification: %w", err)
			}
			if !bytes.Equal(got, hashBytes) {
				os.Remove(partPath)
				return fmt.Errorf("hash mismatch — file may be corrupted")
			}

			if err := os.Rename(partPath, savePath); err != nil {
				return fmt.Errorf("rename %s → %s: %w", partPath, savePath, err)
			}
			return nil

		case typeAbort:
			return fmt.Errorf("sender aborted the transfer")
		}
	}
}

// Abort sends an ABORT packet to the peer to signal that we are cancelling.
func Abort(conn *net.UDPConn, remote *net.UDPAddr) {
	pkt := makePacket(typeAbort, 0, nil)
	conn.WriteToUDP(pkt, remote) //nolint:errcheck
}

// makePacket builds a wire packet: [type(1)][seq(4)][payload].
func makePacket(pktType byte, seq uint32, payload []byte) []byte {
	buf := make([]byte, headerSize+len(payload))
	buf[0] = pktType
	binary.BigEndian.PutUint32(buf[1:5], seq)
	copy(buf[headerSize:], payload)
	return buf
}

// fileHash computes the SHA-256 hash of the file at path.
func fileHash(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
