package cmd

import (
	"fmt"
	"net"
	"os"

	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/stun"
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
	myName := promptName()

	rawToken := args[0]

	payload, err := token.Decode(rawToken)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Discovering your public address via STUN...")

	conn, err := punch.BindSocket()
	if err != nil {
		return err
	}

	diag, err := stun.CheckNAT(conn)
	if err != nil {
		return fmt.Errorf("STUN discovery failed: %w", err)
	}
	myPublicIP, myPublicPort := diag.PublicIP, diag.PublicPort
	fmt.Fprintf(os.Stderr, "Your public address: %s:%d\n", myPublicIP, myPublicPort)
	fmt.Fprintf(os.Stderr, "Peer's public address: %s:%d\n", payload.IP, payload.Port)
	printNATDiag(diag)

	replyPayload, err := token.NewReplyPayload(myPublicIP, myPublicPort, payload.Session)
	if err != nil {
		return fmt.Errorf("could not create reply token: %w", err)
	}

	replyTok, err := token.Encode(replyPayload)
	if err != nil {
		return fmt.Errorf("could not encode reply token: %w", err)
	}

	replyWords := token.Words(replyTok)
	fmt.Printf("\nReply token: %s\n", replyWords)
	offerClipboard(replyWords)
	fmt.Println("Send this back to your peer over WhatsApp/Signal.")
	fmt.Fprintln(os.Stderr, "\nPunching through NAT...")

	remote := &net.UDPAddr{
		IP:   net.ParseIP(payload.IP),
		Port: int(payload.Port),
	}

	// Simultaneous starts probing immediately — this keeps the NAT hole open
	// while the peer reads the reply token and enters it.
	result, err := punch.Simultaneous(conn, remote, func(msg string) {
		fmt.Fprintf(os.Stderr, "\r  %s        ", msg)
	})
	fmt.Fprintln(os.Stderr) // newline after progress line
	if err != nil {
		return punchError(err, diag)
	}

	fmt.Fprintln(os.Stderr, "Connected. Direct P2P. No server.\n")
	return runChat(result, payload.SessionHex(), myName, fmt.Sprintf("%s:%d", myPublicIP, myPublicPort))
}
