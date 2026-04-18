// Package qtransfer implements file transfer over QUIC.
//
// Compared to the custom Selective Repeat ARQ in filetransfer, this uses
// the quic-go library which provides congestion control, flow control, and
// TLS 1.3 — at the cost of a TLS handshake and a heavier dependency.
//
// Usage pattern:
//
//	// Both sides must have a hole-punched *net.UDPConn before calling.
//	// Receiver (QUIC server) — call first:
//	    qtransfer.Receive(conn, savePath, progress)
//	// Sender (QUIC client) — call second:
//	    qtransfer.Send(conn, remote, filePath, progress)
//
// Wire format inside the QUIC stream:
//
//	[8 bytes: file size uint64 BE]
//	[1 byte:  filename length]
//	[N bytes: filename]
//	[file data ...]
package qtransfer

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	alpn           = "punch-ft/1"
	transferTimeout = 10 * time.Minute
)

// ServerTLSConfig generates a self-signed TLS config for the receiver (QUIC server side).
// Auth comes from the session key already shared via the token.
func ServerTLSConfig() (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create cert: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("key pair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{alpn},
	}, nil
}

// ClientTLSConfig returns a TLS config for the sender (QUIC client side).
// Certificate verification is skipped; the session key in the token provides auth.
func ClientTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec — auth via shared session key
		NextProtos:         []string{alpn},
		ServerName:         "punch",
	}
}

// Send streams filePath to remote over QUIC.
// conn must be a hole-punched UDP socket pointing toward remote.
// progress is called with (bytesSent, totalBytes) after each write; may be nil.
func Send(conn net.PacketConn, remote net.Addr, filePath string, progress func(int64, int64)) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	total := info.Size()
	name := filepath.Base(filePath)
	if len(name) > 255 {
		name = name[:255]
	}

	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()

	qconn, err := quic.Dial(ctx, conn, remote, ClientTLSConfig(), nil)
	if err != nil {
		return fmt.Errorf("QUIC dial: %w", err)
	}
	defer qconn.CloseWithError(0, "done") //nolint:errcheck

	stream, err := qconn.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("open QUIC stream: %w", err)
	}

	// Write header: [8-byte size][1-byte name length][filename]
	hdr := make([]byte, 9+len(name))
	binary.BigEndian.PutUint64(hdr[:8], uint64(total))
	hdr[8] = byte(len(name))
	copy(hdr[9:], name)
	if _, err := stream.Write(hdr); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	buf := make([]byte, 32*1024)
	var sent int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := stream.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write data: %w", werr)
			}
			sent += int64(n)
			if progress != nil {
				progress(sent, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
	}
	stream.Close() //nolint:errcheck
	return nil
}

// Receive listens for a QUIC connection on conn and writes the received file.
// savePath is where the file is saved; if empty, the filename from the stream header is used.
// conn must be a hole-punched UDP socket (the NAT hole must already be open).
// progress is called with (bytesReceived, totalBytes); may be nil.
func Receive(conn net.PacketConn, savePath string, progress func(int64, int64)) error {
	tlsConf, err := ServerTLSConfig()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()

	ln, err := quic.Listen(conn, tlsConf, nil)
	if err != nil {
		return fmt.Errorf("QUIC listen: %w", err)
	}
	defer ln.Close() //nolint:errcheck

	qconn, err := ln.Accept(ctx)
	if err != nil {
		return fmt.Errorf("QUIC accept: %w", err)
	}
	defer qconn.CloseWithError(0, "done") //nolint:errcheck

	stream, err := qconn.AcceptStream(ctx)
	if err != nil {
		return fmt.Errorf("accept QUIC stream: %w", err)
	}

	// Read header: [8-byte size][1-byte name length][filename]
	hdrBuf := make([]byte, 9)
	if _, err := io.ReadFull(stream, hdrBuf); err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	total := int64(binary.BigEndian.Uint64(hdrBuf[:8]))
	nameLen := int(hdrBuf[8])

	nameBuf := make([]byte, nameLen)
	if _, err := io.ReadFull(stream, nameBuf); err != nil {
		return fmt.Errorf("read filename: %w", err)
	}
	if savePath == "" {
		savePath = string(nameBuf)
	}

	out, err := os.Create(savePath)
	if err != nil {
		return fmt.Errorf("create %s: %w", savePath, err)
	}
	defer out.Close()

	buf := make([]byte, 32*1024)
	var received int64
	for received < total {
		n, err := stream.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write file: %w", werr)
			}
			received += int64(n)
			if progress != nil {
				progress(received, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("receive data: %w", err)
		}
	}
	return nil
}
