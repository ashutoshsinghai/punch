// Package filetransfer implements a Selective Repeat ARQ file transfer
// protocol over a raw UDP connection.
//
// Unlike Go-Back-N, only the specific lost chunk is retransmitted —
// out-of-order chunks are buffered on the receiver side. This gives
// significantly better throughput on lossy or high-latency paths.
//
// Wire packet layout:
//
//	DATA:  [0x01][4-byte seq BE][encrypted chunk]
//	ACK:   [0x02][4-byte seq BE]  — individual ACK per chunk
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
	// Must fit in one UDP datagram:
	//   Ethernet MTU 1500 - IP(20) - UDP(8) - pkt header(5) - ChaCha20(28) = 1439
	// Use 1400 for headroom (PPPoE, VPN tunnels, etc.).
	ChunkSize = 1400

	// WindowSize is the max number of unacknowledged chunks in flight.
	WindowSize = 64

	// retransmitTimeout is how long to wait for an individual ACK before
	// resending that specific chunk. Shorter = faster recovery from loss.
	retransmitTimeout = 400 * time.Millisecond

	// pacingDelay is the gap between consecutive DATA sends.
	// 500 µs → ceiling ≈ 1400 B / 500 µs = 2.8 MB/s; keeps NAT buffers happy.
	pacingDelay = 500 * time.Microsecond

	transferDeadline = 90 * time.Second
)

const headerSize = 5 // type(1) + seq(4)

const maxPacket = headerSize + ChunkSize + 28 // +28 for ChaCha20 overhead

// slot tracks a single in-flight chunk on the sender side.
type slot struct {
	enc    []byte    // cached encrypted payload (reused on retransmit)
	acked  bool
	sentAt time.Time
}

// Send streams the file at filePath to remote using Selective Repeat ARQ.
func Send(conn *net.UDPConn, remote *net.UDPAddr, filePath string, cipher *crypto.Cipher, progress func(int64, int64)) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()

	// First pass: compute SHA-256 and total size.
	hasher := sha256.New()
	totalSize, err := io.Copy(hasher, f)
	if err != nil {
		return fmt.Errorf("hash %s: %w", filePath, err)
	}
	fileHash := hasher.Sum(nil)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek: %w", err)
	}

	totalChunks := uint32(0)
	if totalSize > 0 {
		totalChunks = uint32((totalSize + ChunkSize - 1) / ChunkSize)
	}

	// Goroutine: drain ACKs into a channel.
	ackCh := make(chan uint32, WindowSize*4)
	stopACK := make(chan struct{})
	go func() {
		buf := make([]byte, 9)
		for {
			select {
			case <-stopACK:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(transferDeadline))
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			if addr.String() != remote.String() || n < headerSize || buf[0] != typeACK {
				continue
			}
			seq := binary.BigEndian.Uint32(buf[1:5])
			select {
			case ackCh <- seq:
			default:
			}
		}
	}()
	defer close(stopACK)

	inFlight := make(map[uint32]*slot, WindowSize)
	windowBase := uint32(0)
	nextSeq := uint32(0)
	chunkBuf := make([]byte, ChunkSize)

	readEncChunk := func(seq uint32) ([]byte, error) {
		n, err := f.ReadAt(chunkBuf, int64(seq)*ChunkSize)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("read chunk %d: %w", seq, err)
		}
		enc, err := cipher.Encrypt(chunkBuf[:n])
		if err != nil {
			return nil, fmt.Errorf("encrypt chunk %d: %w", seq, err)
		}
		return enc, nil
	}

	sendChunk := func(seq uint32, enc []byte) {
		conn.WriteToUDP(makePacket(typeData, seq, enc), remote) //nolint:errcheck
	}

	advanceWindow := func() {
		for {
			s, ok := inFlight[windowBase]
			if !ok || !s.acked {
				break
			}
			delete(inFlight, windowBase)
			windowBase++
		}
	}

	drainACKs := func() {
		for {
			select {
			case ack := <-ackCh:
				if s, ok := inFlight[ack]; ok {
					s.acked = true
				}
			default:
				return
			}
		}
	}

	retransmitTicker := time.NewTicker(100 * time.Millisecond)
	defer retransmitTicker.Stop()

	for windowBase < totalChunks {
		// Process any queued ACKs and slide the window.
		drainACKs()
		advanceWindow()

		// Report progress.
		if progress != nil {
			sent := int64(windowBase) * ChunkSize
			if sent > totalSize {
				sent = totalSize
			}
			progress(sent, totalSize)
		}

		// Retransmit any chunks that timed out (Selective Repeat: only lost ones).
		select {
		case <-retransmitTicker.C:
			now := time.Now()
			for seq, s := range inFlight {
				if !s.acked && now.Sub(s.sentAt) > retransmitTimeout {
					sendChunk(seq, s.enc)
					s.sentAt = now
				}
			}
		default:
		}

		// Fill window with new chunks (paced).
		for nextSeq < windowBase+WindowSize && nextSeq < totalChunks {
			if _, exists := inFlight[nextSeq]; !exists {
				enc, err := readEncChunk(nextSeq)
				if err != nil {
					return err
				}
				sendChunk(nextSeq, enc)
				inFlight[nextSeq] = &slot{enc: enc, sentAt: time.Now()}
				time.Sleep(pacingDelay)
			}
			nextSeq++
		}

		// If window is full, block until an ACK arrives or retransmit fires.
		if uint32(len(inFlight)) >= WindowSize {
			select {
			case ack := <-ackCh:
				if s, ok := inFlight[ack]; ok {
					s.acked = true
				}
				advanceWindow()
			case <-retransmitTicker.C:
				now := time.Now()
				for seq, s := range inFlight {
					if !s.acked && now.Sub(s.sentAt) > retransmitTimeout {
						sendChunk(seq, s.enc)
						s.sentAt = now
					}
				}
			}
		}
	}

	// Send EOF with encrypted hash; retry to survive loss.
	hashEnc, err := cipher.Encrypt(fileHash)
	if err != nil {
		return fmt.Errorf("encrypt hash: %w", err)
	}
	eofPkt := makePacket(typeEOF, 0, hashEnc)
	for i := 0; i < 20; i++ {
		conn.WriteToUDP(eofPkt, remote) //nolint:errcheck
		time.Sleep(100 * time.Millisecond)
	}

	if progress != nil {
		progress(totalSize, totalSize)
	}
	return nil
}

// Receive reads chunks from remote, buffers out-of-order ones, writes them
// in order to savePath, and verifies the SHA-256 hash in the EOF packet.
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
	outOfOrder := make(map[uint32][]byte) // seq → plaintext
	buf := make([]byte, maxPacket)

	sendACK := func(seq uint32) {
		conn.WriteToUDP(makePacket(typeACK, seq, nil), remote) //nolint:errcheck
	}

	// Drain outOfOrder buffer: write consecutive chunks starting at expected.
	drainBuffer := func() error {
		for {
			plain, ok := outOfOrder[expected]
			if !ok {
				break
			}
			if _, err := f.Write(plain); err != nil {
				return fmt.Errorf("write chunk %d: %w", expected, err)
			}
			received += int64(len(plain))
			delete(outOfOrder, expected)
			expected++
		}
		return nil
	}

	for {
		conn.SetReadDeadline(time.Now().Add(transferDeadline))
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return fmt.Errorf("receive timeout (no data for %s)", transferDeadline)
		}
		if addr.String() != remote.String() || n < headerSize {
			continue
		}

		pktType := buf[0]
		seq := binary.BigEndian.Uint32(buf[1:5])
		payload := buf[headerSize:n]

		switch pktType {
		case typeData:
			plain, err := cipher.Decrypt(payload)
			if err != nil {
				// Bad decrypt — likely a stray packet; ignore.
				continue
			}

			// Always ACK so the sender knows this chunk arrived.
			sendACK(seq)

			if seq < expected {
				// Duplicate — ACK already sent above, nothing else to do.
				continue
			}
			if seq == expected {
				if _, err := f.Write(plain); err != nil {
					return fmt.Errorf("write chunk %d: %w", seq, err)
				}
				received += int64(len(plain))
				expected++
				if err := drainBuffer(); err != nil {
					return err
				}
			} else {
				// Out-of-order: buffer it (only if not already buffered).
				if _, exists := outOfOrder[seq]; !exists {
					cp := make([]byte, len(plain))
					copy(cp, plain)
					outOfOrder[seq] = cp
				}
			}

			if progress != nil {
				progress(received, totalSize)
			}

		case typeEOF:
			hashBytes, err := cipher.Decrypt(payload)
			if err != nil {
				return fmt.Errorf("decrypt EOF hash: %w", err)
			}
			f.Close()
			closed = true

			got, err := fileHash(partPath)
			if err != nil {
				return fmt.Errorf("hash verify: %w", err)
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

// Abort sends an ABORT packet to signal cancellation.
func Abort(conn *net.UDPConn, remote *net.UDPAddr) {
	conn.WriteToUDP(makePacket(typeAbort, 0, nil), remote) //nolint:errcheck
}

func makePacket(pktType byte, seq uint32, payload []byte) []byte {
	buf := make([]byte, headerSize+len(payload))
	buf[0] = pktType
	binary.BigEndian.PutUint32(buf[1:5], seq)
	copy(buf[headerSize:], payload)
	return buf
}

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
