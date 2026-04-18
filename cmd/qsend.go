package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/qtransfer"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var qsendCmd = &cobra.Command{
	Use:   "qsend <file> <token>",
	Short: "Send a file via QUIC (benchmark companion to qreceive)",
	Args:  cobra.ExactArgs(2),
	RunE:  runQSend,
}

func init() {
	rootCmd.AddCommand(qsendCmd)
}

func runQSend(_ *cobra.Command, args []string) error {
	filePath := args[0]
	rawToken := args[1]

	payload, err := token.Decode(rawToken)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("cannot stat file %q: %w", filePath, err)
	}

	fmt.Fprintf(os.Stderr, "Connecting to %s port %d...\n", payload.IP, payload.Port)

	conn, remote, err := punch.DialRaw(payload.IP, payload.Port)
	if err != nil {
		return err
	}

	fmt.Printf("Sending %s (%s) via QUIC...\n", info.Name(), humanBytes(int(info.Size())))
	start := time.Now()

	err = qtransfer.Send(conn, remote, filePath, func(sent, total int64) {
		if total > 0 {
			printProgress(int(sent), int(total))
		}
	})
	if err != nil {
		return fmt.Errorf("QUIC transfer failed: %w", err)
	}

	elapsed := time.Since(start)
	speed := float64(info.Size()) / elapsed.Seconds()
	fmt.Printf("\nDone. %s sent in %s — %s/s\n",
		humanBytes(int(info.Size())),
		elapsed.Round(time.Millisecond),
		humanBytes(int(speed)),
	)
	return nil
}
