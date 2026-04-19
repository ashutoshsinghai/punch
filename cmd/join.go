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

	// ── Step 1: Decode peer's token ───────────────────────────────────────────
	rawToken := args[0]
	payload, err := token.Decode(rawToken)
	if err != nil {
		return stepFail("token", "invalid token — "+err.Error())
	}
	stepOK("peer address", fmt.Sprintf("%s:%d", payload.IP, payload.Port))

	// ── Step 2: STUN ──────────────────────────────────────────────────────────
	step("STUN", "discovering your public address...")
	conn, err := punch.BindSocket()
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	diag, err := stun.CheckNAT(conn)
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	myPublicIP, myPublicPort := diag.PublicIP, diag.PublicPort
	stepOK("STUN", fmt.Sprintf("your address is %s:%d", myPublicIP, myPublicPort))

	// ── Step 3: NAT type ──────────────────────────────────────────────────────
	natLabel, natWarn := natDiagLine(diag)
	if natWarn {
		stepWarn("NAT type", natLabel)
	} else {
		stepOK("NAT type", natLabel)
	}

	// ── Step 4: Reply token ───────────────────────────────────────────────────
	replyPayload, err := token.NewReplyPayload(myPublicIP, myPublicPort, payload.Session)
	if err != nil {
		return stepFail("reply token", err.Error())
	}
	replyTok, err := token.Encode(replyPayload)
	if err != nil {
		return stepFail("reply token", err.Error())
	}
	replyWords := token.Words(replyTok)
	fmt.Printf("\nReply token: %s\n", replyWords)
	offerClipboard(replyWords)
	fmt.Println("Send this back to your peer over WhatsApp/Signal.")

	// ── Step 5: Hole punch ────────────────────────────────────────────────────
	remote := &net.UDPAddr{IP: net.ParseIP(payload.IP), Port: int(payload.Port)}
	step("hole punch", "probing peer...")

	result, err := punch.Simultaneous(conn, remote, func(msg string) {
		fmt.Fprintf(os.Stderr, "\r[   ] hole punch    — %s        ", msg)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return stepFail("hole punch", punchReason(diag))
	}
	stepOK("hole punch", "connected — direct P2P, no server")

	fmt.Fprintln(os.Stderr)
	return runChat(result, payload.SessionHex(), myName, fmt.Sprintf("%s:%d", myPublicIP, myPublicPort))
}
