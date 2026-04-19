package cmd

import (
	"fmt"
	"os"

	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/internal/stun"
	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Check if your network supports direct P2P connections",
	RunE:  runCheck,
}

func init() {
	rootCmd.AddCommand(checkCmd)
}

func runCheck(_ *cobra.Command, _ []string) error {
	fmt.Fprintln(os.Stderr)

	// ── STUN ─────────────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] STUN\n")
	fmt.Fprintf(os.Stderr, "      → querying %s...\n", stun.Server)
	conn, err := punch.BindSocket()
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	defer conn.Close()
	diag, err := stun.CheckNAT(conn)
	if err != nil {
		return stepFail("STUN", err.Error())
	}
	fmt.Fprintf(os.Stderr, "      → your public address: %s:%d\n", diag.PublicIP, diag.PublicPort)
	fmt.Fprintf(os.Stderr, "        (this is what the internet sees for your UDP socket)\n")
	stepOK("STUN", "")

	// ── NAT type ──────────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] NAT type\n")
	fmt.Fprintf(os.Stderr, "      → server 1 (%s)  mapped port: %d\n", stun.Server, diag.PublicPort)
	if diag.PublicPort2 != 0 {
		sameOrDiff := "same ✓"
		if diag.IsSymmetric {
			sameOrDiff = "DIFFERENT ✗"
		}
		fmt.Fprintf(os.Stderr, "      → server 2 (%s) mapped port: %d  (%s)\n",
			stun.Server2, diag.PublicPort2, sameOrDiff)
	} else {
		fmt.Fprintf(os.Stderr, "      → server 2 query failed (skipping symmetric NAT check)\n")
	}
	if diag.IsCGNAT {
		fmt.Fprintf(os.Stderr, "      → %s is in the CGNAT range (RFC 6598: 100.64.0.0/10)\n", diag.PublicIP)
		fmt.Fprintf(os.Stderr, "        your ISP has put you behind their own NAT — two NATs between you and the internet\n")
		fmt.Fprintf(os.Stderr, "        UDP hole punching cannot reliably work through double NAT\n")
		fmt.Fprintf(os.Stderr, "        tip: switch to a mobile hotspot\n")
		stepWarn("NAT type", "CGNAT detected")
	} else if diag.IsSymmetric {
		fmt.Fprintf(os.Stderr, "      → your router assigns a different external port per destination\n")
		fmt.Fprintf(os.Stderr, "        the peer cannot predict which port to send packets back to\n")
		fmt.Fprintf(os.Stderr, "        tip: switch to a mobile hotspot or set router NAT mode to Full Cone\n")
		stepWarn("NAT type", "symmetric NAT detected")
	} else {
		fmt.Fprintf(os.Stderr, "      → both servers see the same port — NAT is not symmetric\n")
		stepOK("NAT type", "port-restricted, hole punching should work")
	}

	// ── Verdict ───────────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "[   ] verdict\n")
	if diag.IsCGNAT || diag.IsSymmetric {
		fmt.Fprintf(os.Stderr, "      → your network will likely block hole punching\n")
		fmt.Fprintf(os.Stderr, "      → tip: switch to a mobile hotspot if it fails\n")
		stepWarn("verdict", "likely to fail on your side")
	} else {
		fmt.Fprintf(os.Stderr, "      → your network supports direct P2P connections\n")
		stepOK("verdict", "your side is ready")
	}
	fmt.Fprintln(os.Stderr)

	return nil
}
