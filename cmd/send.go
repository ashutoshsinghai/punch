package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/ashutoshsinghai/punch/internal/crypto"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var sendCmd = &cobra.Command{
	Use:   "send <file> <token>",
	Short: "Send a file directly to a peer",
	Args:  cobra.ExactArgs(2),
	RunE:  runSend,
}

func init() {
	rootCmd.AddCommand(sendCmd)
}

func runSend(_ *cobra.Command, args []string) error {
	filePath := args[0]
	rawToken := args[1]

	payload, err := token.Decode(rawToken)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("cannot read file %q: %w", filePath, err)
	}

	// Compute hash before sending so receiver can verify.
	hash := sha256.Sum256(data)
	hashHex := hex.EncodeToString(hash[:])

	fileInfo, _ := os.Stat(filePath)
	fileName := fileInfo.Name()
	fileSize := len(data)

	fmt.Fprintf(os.Stderr, "Connecting to %s port %d...\n", payload.IP, payload.Port)

	result, err := punch.Dial(payload.IP, payload.Port)
	if err != nil {
		return err
	}
	defer result.Conn.Close()

	cipher, err := crypto.NewCipher(payload.SessionHex())
	if err != nil {
		return err
	}

	// Send metadata frame first: "FILE:<name>:<size>:<sha256>"
	meta := fmt.Sprintf("FILE:%s:%d:%s", fileName, fileSize, hashHex)
	enc, err := cipher.Encrypt([]byte(meta))
	if err != nil {
		return err
	}
	if err := result.Conn.Send(enc); err != nil {
		return fmt.Errorf("failed to send file metadata: %w", err)
	}

	fmt.Printf("Sending %s (%s)...\n", fileName, humanBytes(fileSize))

	// Send file data in chunks.
	sent := 0
	err = result.Conn.SendFile(data, func(n, total int) {
		sent = n
		printProgress(n, total)
	})
	if err != nil {
		return fmt.Errorf("file transfer failed after %d bytes: %w", sent, err)
	}

	// Send EOF sentinel.
	eofEnc, err := cipher.Encrypt([]byte("EOF"))
	if err != nil {
		return err
	}
	if err := result.Conn.Send(eofEnc); err != nil {
		return fmt.Errorf("failed to send EOF: %w", err)
	}

	fmt.Printf("\nDone. %s sent (%s).\n", fileName, humanBytes(fileSize))
	return nil
}

func printProgress(sent, total int) {
	if total == 0 {
		return
	}
	pct := sent * 100 / total
	filled := pct / 5
	bar := ""
	for i := 0; i < 20; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	fmt.Printf("\r→ %s %d%%  ", bar, pct)
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
