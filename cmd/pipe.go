package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/ashutoshsinghai/punch/internal/crypto"
	"github.com/ashutoshsinghai/punch/internal/ip"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/token"
	"github.com/spf13/cobra"
)

var pipeCmd = &cobra.Command{
	Use:   "pipe [token]",
	Short: "Pipe stdin to a peer, or receive a pipe from a peer",
	Long: `pipe with a token: reads stdin and streams it to the peer.
pipe without a token: generates a token, waits for peer, writes received data to stdout.

Examples:
  kubectl logs pod | punch pipe              # generate token, stream logs
  punch pipe ABCD-1234-EFGH > output.txt     # receive the stream`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPipe,
}

func init() {
	rootCmd.AddCommand(pipeCmd)
}

func runPipe(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return pipeReceive()
	}
	return pipeSend(args[0])
}

// pipeReceive: generate token, wait for sender, write to stdout.
func pipeReceive() error {
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

	fmt.Fprintf(os.Stderr, "\nToken: %s\n", token.Words(tok))
	fmt.Fprintln(os.Stderr, "Waiting for peer to pipe data...")

	result, err := punch.Listen(port)
	if err != nil {
		return err
	}
	defer result.Conn.Close()

	cipher, err := crypto.NewCipher(payload.SessionHex())
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Connected. Streaming...")

	for {
		raw, err := result.Conn.Recv()
		if err != nil {
			return nil // peer closed
		}
		plain, err := cipher.Decrypt(raw)
		if err != nil {
			return fmt.Errorf("decryption failed: %w", err)
		}
		if string(plain) == "EOF" {
			return nil
		}
		if _, err := os.Stdout.Write(plain); err != nil {
			return err
		}
	}
}

// pipeSend: connect to peer using token, stream stdin.
func pipeSend(rawToken string) error {
	payload, err := token.Decode(rawToken)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Connecting to %s:%d...\n", payload.IP, payload.Port)

	result, err := punch.Dial(payload.IP, payload.Port)
	if err != nil {
		return err
	}
	defer result.Conn.Close()

	cipher, err := crypto.NewCipher(payload.SessionHex())
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Connected. Piping stdin...")

	buf := make([]byte, 8192)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			enc, encErr := cipher.Encrypt(buf[:n])
			if encErr != nil {
				return fmt.Errorf("encryption failed: %w", encErr)
			}
			if sendErr := result.Conn.Send(enc); sendErr != nil {
				return fmt.Errorf("send failed: %w", sendErr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("stdin read error: %w", err)
		}
	}

	// Send EOF sentinel.
	eofEnc, err := cipher.Encrypt([]byte("EOF"))
	if err != nil {
		return err
	}
	return result.Conn.Send(eofEnc)
}
