package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/ashutoshsinghai/punch/internal/ip"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/qtransfer"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var qreceiveCmd = &cobra.Command{
	Use:   "qreceive",
	Short: "Receive a file via QUIC (benchmark companion to qsend)",
	Args:  cobra.NoArgs,
	RunE:  runQReceive,
}

func init() {
	rootCmd.AddCommand(qreceiveCmd)
}

func runQReceive(_ *cobra.Command, _ []string) error {
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
	fmt.Println("Send this to your peer. They run: punch qsend <file> <token>")
	fmt.Fprintln(os.Stderr, "\nWaiting for sender...")

	conn, _, err := punch.ListenRaw(port)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Connected. Receiving via QUIC...")
	start := time.Now()
	var lastName string

	err = qtransfer.Receive(conn, "", func(recv, total int64) {
		if total > 0 {
			pct := int(recv * 100 / total)
			filled := pct / 5
			bar := make([]rune, 20)
			for i := range bar {
				if i < filled {
					bar[i] = '█'
				} else {
					bar[i] = '░'
				}
			}
			fmt.Printf("\r→ %s %d%%  ", string(bar), pct)
		}
	})
	if err != nil {
		return fmt.Errorf("QUIC receive failed: %w", err)
	}

	elapsed := time.Since(start)
	_ = lastName
	fmt.Printf("\nDone in %s.\n", elapsed.Round(time.Millisecond))
	return nil
}
