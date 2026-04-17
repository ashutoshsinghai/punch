// Package transport implements a lightweight reliable layer on top of UDP.
//
// UDP is unreliable by nature. This package adds:
//   - Message framing (each write/read is one logical message)
//   - Sequence numbers + ACKs for reliability
//   - Chunked file transfer with a progress callback
//
// The wire format for every packet is:
//
//	[1-byte type][4-byte seq][4-byte payload-len][payload bytes]
//
// Types:
//
//	0x01 DATA   — carries application payload
//	0x02 ACK    — acknowledges seq N
//	0x03 PING   — keepalive / hole-punch probe
//	0x04 PONG   — reply to PING
//	0x05 FIN    — close signal
package transport

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	typeData = 0x01
	typeACK  = 0x02
	typePing = 0x03
	typePong = 0x04
	typeFin  = 0x05

	maxPacketSize = 65507 // max safe UDP payload
	ackTimeout    = 2 * time.Second
	maxRetries    = 30
	readDeadline  = 60 * time.Second
)

// Conn is a reliable UDP connection to a single remote peer.
type Conn struct {
	conn    *net.UDPConn
	remote  *net.UDPAddr
	sendSeq uint32
	recvSeq uint32
	mu      sync.Mutex

	// pending maps seq → channel that receives ACK
	pending map[uint32]chan struct{}
	pendMu  sync.Mutex

	closed atomic.Bool
	recv   chan []byte // incoming DATA payloads
}

// keepaliveInterval is how often a PING is sent to keep the NAT hole open.
const keepaliveInterval = 10 * time.Second

// Wrap takes an already-connected UDP socket and builds a Conn around it.
func Wrap(conn *net.UDPConn, remote *net.UDPAddr) *Conn {
	c := &Conn{
		conn:    conn,
		remote:  remote,
		pending: make(map[uint32]chan struct{}),
		recv:    make(chan []byte, 512),
	}
	go c.readLoop()
	go c.keepaliveLoop()
	return c
}

// keepaliveLoop sends a PING every keepaliveInterval to keep the NAT hole open.
func (c *Conn) keepaliveLoop() {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if c.closed.Load() {
				return
			}
			c.Ping() //nolint:errcheck
		}
	}
}

// Send sends payload reliably (with retransmit until ACKed).
func (c *Conn) Send(payload []byte) error {
	c.mu.Lock()
	seq := c.sendSeq
	c.sendSeq++
	c.mu.Unlock()

	ackCh := make(chan struct{}, 1)
	c.pendMu.Lock()
	c.pending[seq] = ackCh
	c.pendMu.Unlock()

	defer func() {
		c.pendMu.Lock()
		delete(c.pending, seq)
		c.pendMu.Unlock()
	}()

	pkt := encodePacket(typeData, seq, payload)

	for i := 0; i < maxRetries; i++ {
		if _, err := c.conn.WriteToUDP(pkt, c.remote); err != nil {
			return fmt.Errorf("send failed: %w", err)
		}

		select {
		case <-ackCh:
			return nil
		case <-time.After(ackTimeout):
			// retransmit
		}
	}
	return fmt.Errorf("send: no ACK after %d retries (peer may be unreachable)", maxRetries)
}

// Recv blocks until a DATA message arrives and returns its payload.
func (c *Conn) Recv() ([]byte, error) {
	payload, ok := <-c.recv
	if !ok {
		return nil, fmt.Errorf("connection closed")
	}
	return payload, nil
}

// RecvCh returns the channel of incoming payloads (for select-based use).
func (c *Conn) RecvCh() <-chan []byte {
	return c.recv
}

// Ping sends a keepalive/hole-punch probe (unreliable, fire-and-forget).
func (c *Conn) Ping() error {
	pkt := encodePacket(typePing, 0, nil)
	_, err := c.conn.WriteToUDP(pkt, c.remote)
	return err
}

// Close sends FIN and shuts down the connection.
func (c *Conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	pkt := encodePacket(typeFin, 0, nil)
	c.conn.WriteToUDP(pkt, c.remote) //nolint:errcheck
	close(c.recv)
	return c.conn.Close()
}

// SendFile sends a file's bytes with chunking and calls progress(sent, total) after each chunk.
func (c *Conn) SendFile(data []byte, progress func(sent, total int)) error {
	const chunkSize = 8192
	total := len(data)

	for offset := 0; offset < total; offset += chunkSize {
		end := offset + chunkSize
		if end > total {
			end = total
		}
		if err := c.Send(data[offset:end]); err != nil {
			return err
		}
		if progress != nil {
			progress(end, total)
		}
	}
	return nil
}

// readLoop is the internal receive goroutine.
func (c *Conn) readLoop() {
	buf := make([]byte, maxPacketSize)
	for {
		c.conn.SetReadDeadline(time.Now().Add(readDeadline))
		n, addr, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			if c.closed.Load() {
				return
			}
			continue
		}

		// Only accept packets from our peer.
		if addr.String() != c.remote.String() {
			continue
		}

		if n < 9 { // minimum: 1+4+4
			continue
		}

		pktType := buf[0]
		seq := binary.BigEndian.Uint32(buf[1:5])
		payLen := int(binary.BigEndian.Uint32(buf[5:9]))
		var payload []byte
		if payLen > 0 && n >= 9+payLen {
			payload = make([]byte, payLen)
			copy(payload, buf[9:9+payLen])
		}

		switch pktType {
		case typeData:
			// Send ACK.
			ack := encodePacket(typeACK, seq, nil)
			c.conn.WriteToUDP(ack, c.remote) //nolint:errcheck

			// Deliver to application (simple: no reordering for v1).
			if !c.closed.Load() {
				select {
				case c.recv <- payload:
				default:
					// Drop if buffer full — sender will retransmit.
				}
			}

		case typeACK:
			c.pendMu.Lock()
			ch, ok := c.pending[seq]
			c.pendMu.Unlock()
			if ok {
				select {
				case ch <- struct{}{}:
				default:
				}
			}

		case typePing:
			pong := encodePacket(typePong, 0, nil)
			c.conn.WriteToUDP(pong, c.remote) //nolint:errcheck

		case typeFin:
			c.closed.Store(true)
			if !c.closed.Load() {
				close(c.recv)
			}
			return
		}
	}
}

func encodePacket(pktType byte, seq uint32, payload []byte) []byte {
	buf := make([]byte, 9+len(payload))
	buf[0] = pktType
	binary.BigEndian.PutUint32(buf[1:5], seq)
	binary.BigEndian.PutUint32(buf[5:9], uint32(len(payload)))
	copy(buf[9:], payload)
	return buf
}
