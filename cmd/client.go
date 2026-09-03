package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-agent/agent"
	"github.com/cocoonstack/cocoon-agent/client"
)

// exitCodeError carries the child exit code through cobra's RunE so defers run before os.Exit.
type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

func newClientCmd() *cobra.Command {
	var (
		cid  uint32
		port uint32
	)

	cmd := &cobra.Command{
		Use:   "client [flags] -- <argv>...",
		Short: "Run a command on a remote cocoon-agent (debug / smoke test)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			conn, err := dialVsock(cid, port)
			if err != nil {
				return fmt.Errorf("dial vsock cid=%d port=%d: %w", cid, port, err)
			}
			// client.Run closes conn via its runCancel goroutine, so no defer Close here
			exitCode, err := client.Run(ctx, conn, args, nil, os.Stdin, os.Stdout, os.Stderr)
			if err != nil {
				return err
			}
			if exitCode != 0 {
				return &exitCodeError{code: exitCode}
			}
			return nil
		},
	}
	cmd.Flags().Uint32Var(&cid, "cid", 0, "vsock CID of the target VM (host-side; from hypervisor)")
	cmd.Flags().Uint32Var(&port, "port", agent.DefaultPort, "vsock port the agent is listening on")
	_ = cmd.MarkFlagRequired("cid")
	return cmd
}
