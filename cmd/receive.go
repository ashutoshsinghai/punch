package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ashutoshsinghai/punch/internal/crypto"
	"github.com/ashutoshsinghai/punch/internal/ip"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var receiveCmd = &cobra.Command{
	Use:   "receive <token>",
	Short: "Receive a file from a peer (prints a token for the sender)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runReceive,
}

func init() {
	rootCmd.AddCommand(receiveCmd)
}

func runReceive(_ *cobra.Command, args []string) error {
	// receive with no token → generate one and wait (receiver is the listener).
	var (
		result  *punch.Result
		session string
	)

	if len(args) == 0 {
		// Receiver generates the token and waits.
		publicIP, err := ip.Public()
		if err != nil {
			return err
		}
		port, err := punch.RandomPort()
		if err != nil {
			return err
		}
		payload, err := token.NewPayload(publicIP, port)
		if err != nil {
			return err
		}
		tok, err := token.Encode(payload)
		if err != nil {
			return err
		}
		fmt.Printf("\nToken: %s\n", token.Words(tok))
		fmt.Println("Send this to your peer. They run: punch send <file> <token>")
		fmt.Fprintln(os.Stderr, "\nWaiting for sender...")

		result, err = punch.Listen(port)
		if err != nil {
			return err
		}
		session = payload.SessionHex()

	} else {
		// Receiver dials using sender's token.
		payload, err := token.Decode(args[0])
		if err != nil {
			return fmt.Errorf("invalid token: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Connecting to %s port %d...\n", payload.IP, payload.Port)
		result, err = punch.Dial(payload.IP, payload.Port)
		if err != nil {
			return err
		}
		session = payload.SessionHex()
	}
	defer result.Conn.Close()

	cipher, err := crypto.NewCipher(session)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Connected. Waiting for file metadata...")

	// Receive metadata frame.
	raw, err := result.Conn.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive metadata: %w", err)
	}
	meta, err := cipher.Decrypt(raw)
	if err != nil {
		return fmt.Errorf("metadata decryption failed: %w", err)
	}

	parts := strings.SplitN(string(meta), ":", 4)
	if len(parts) != 4 || parts[0] != "FILE" {
		return fmt.Errorf("unexpected metadata: %s", meta)
	}
	fileName := parts[1]
	fileSize, err := strconv.Atoi(parts[2])
	if err != nil {
		return fmt.Errorf("invalid file size in metadata")
	}
	expectedHash := parts[3]

	fmt.Printf("Receiving %s (%s)...\n", fileName, humanBytes(fileSize))

	var fileData []byte
	start := time.Now()

	for {
		chunk, err := result.Conn.Recv()
		if err != nil {
			return fmt.Errorf("receive error: %w", err)
		}
		plain, err := cipher.Decrypt(chunk)
		if err != nil {
			return fmt.Errorf("chunk decryption failed: %w", err)
		}
		if string(plain) == "EOF" {
			break
		}
		fileData = append(fileData, plain...)
		printProgress(len(fileData), fileSize)
	}

	fmt.Println()

	// Verify hash.
	got := sha256.Sum256(fileData)
	gotHex := hex.EncodeToString(got[:])
	if gotHex != expectedHash {
		return fmt.Errorf("hash mismatch — file may be corrupted (expected %s, got %s)", expectedHash, gotHex)
	}

	if err := os.WriteFile(fileName, fileData, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	elapsed := time.Since(start)
	fmt.Printf("Done. %s received in %s. Hash verified.\n", fileName, elapsed.Round(time.Millisecond))
	return nil
}
