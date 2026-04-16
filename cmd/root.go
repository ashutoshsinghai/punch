package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via goreleaser ldflags (-X main.version=...).
// main.go assigns it here before Execute() is called.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "punch",
	Short: "Direct P2P connections between any two machines, anywhere",
	Long: `punch — NAT hole punching CLI

Connect two machines directly over UDP. No server. No accounts.
Share a short token over WhatsApp or chat, and you're connected.

Examples:
  punch share                    Start a session, get a token
  punch join RIVER-4421-STONE    Connect to a peer
  punch send report.pdf <token>  Send a file
  punch receive                  Receive a file (generates a token)
  kubectl logs pod | punch pipe  Stream stdin to a peer`,
	SilenceUsage: true,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the current punch version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("punch", Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(upgradeCmd)
	rootCmd.AddCommand(installCmd)
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
