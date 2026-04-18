package cmd

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/stun"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var shareFormat string

var shareCmd = &cobra.Command{
	Use:   "share",
	Short: "Start a session and print a token for your peer",
	RunE:  runShare,
}

func init() {
	shareCmd.Flags().StringVar(&shareFormat, "token", "words", "Token format: words | base58")
	rootCmd.AddCommand(shareCmd)
}

func runShare(_ *cobra.Command, _ []string) error {
	myName := promptName()

	fmt.Fprintln(os.Stderr, "Discovering your public address via STUN...")

	conn, err := punch.BindSocket()
	if err != nil {
		return err
	}

	publicIP, publicPort, err := stun.Discover(conn)
	if err != nil {
		return fmt.Errorf("STUN discovery failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Your public address: %s:%d\n", publicIP, publicPort)

	payload, err := token.NewPayload(publicIP, publicPort)
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
	fmt.Print("Press 'c' to copy to clipboard, any other key to skip: ")
	if key := readSingleKey(); key == 'c' || key == 'C' {
		if err := clipboard.WriteAll(display); err == nil {
			fmt.Println("copied!")
		} else {
			fmt.Println("copy failed: " + err.Error())
		}
	} else {
		fmt.Println()
	}
	fmt.Println("Send this to your peer over WhatsApp/Signal.\n")
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
	return runChat(result, payload.SessionHex(), myName, fmt.Sprintf("%s:%d", publicIP, publicPort))
}

// readSingleKey reads one keypress without requiring Enter.
// Falls back to reading a full line if the terminal isn't a TTY.
func readSingleKey() byte {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
			buf := make([]byte, 1)
			os.Stdin.Read(buf) //nolint:errcheck
			return buf[0]
		}
	}
	// Fallback: read a line and return the first character.
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	if len(strings.TrimSpace(line)) > 0 {
		return line[0]
	}
	return 0
}
