package cmd

import (
	"fmt"
	"os"

	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var joinCmd = &cobra.Command{
	Use:   "join <token>",
	Short: "Connect to a peer using their token",
	Args:  cobra.ExactArgs(1),
	RunE:  runJoin,
}

func init() {
	rootCmd.AddCommand(joinCmd)
}

func runJoin(_ *cobra.Command, args []string) error {
	rawToken := args[0]

	payload, err := token.Decode(rawToken)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Punching through NAT to %s (local: %s) port %d...\n", payload.IP, payload.LocalIP, payload.Port)

	result, err := punch.Dial(payload.IP, payload.LocalIP, payload.Port)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Connected. Direct P2P. No server.\n")

	return runChat(result, payload.SessionHex())
}
