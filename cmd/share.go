package cmd

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/stun"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var (
	shareExpire string
	shareFormat string
)

var shareCmd = &cobra.Command{
	Use:   "share",
	Short: "Start a session and print a token for your peer",
	RunE:  runShare,
}

func init() {
	shareCmd.Flags().StringVar(&shareExpire, "expire", "10m", "Token expiry duration (e.g. 10m, 30m, 1h)")
	shareCmd.Flags().StringVar(&shareFormat, "token", "words", "Token format: words | base58")
	rootCmd.AddCommand(shareCmd)
}

func runShare(_ *cobra.Command, _ []string) error {
	expiry, err := time.ParseDuration(shareExpire)
	if err != nil {
		return fmt.Errorf("invalid expiry %q: %w", shareExpire, err)
	}

	fmt.Fprintln(os.Stderr, "Discovering your public address via STUN...")

	conn, err := punch.BindSocket()
	if err != nil {
		return err
	}

	publicIP, publicPort, err := stun.Discover(conn)
	if err != nil {
		return fmt.Errorf("STUN discovery failed: %w", err)
	}

	payload, err := token.NewPayload(publicIP, publicPort, expiry)
	if err != nil {
		return fmt.Errorf("could not create token: %w", err)
	}

	tok, err := token.Encode(payload)
	if err != nil {
		return fmt.Errorf("could not encode token: %w", err)
	}

	display := tok
	if shareFormat == "words" {
		display = token.Words(tok)
	}

	fmt.Printf("\nToken: %s\n", display)
	fmt.Printf("Send this to your peer over WhatsApp/Signal. Expires in %s.\n\n", expiry)
	fmt.Print("Peer's reply token: ")

	reader := bufio.NewReader(os.Stdin)
	replyTok, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read reply token: %w", err)
	}
	replyTok = strings.TrimSpace(replyTok)

	replyPayload, err := token.Decode(replyTok)
	if err != nil {
		return fmt.Errorf("invalid reply token: %w", err)
	}

	if replyPayload.Session != payload.Session {
		return fmt.Errorf("session mismatch — make sure your peer used your token")
	}

	remote := &net.UDPAddr{
		IP:   net.ParseIP(replyPayload.IP),
		Port: int(replyPayload.Port),
	}

	fmt.Fprintln(os.Stderr, "\nPunching through NAT...")

	result, err := punch.Simultaneous(conn, remote)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Connected. Direct P2P. No server.\n")
	return runChat(result, payload.SessionHex())
}
