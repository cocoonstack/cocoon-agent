package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-agent/agent"
	"github.com/cocoonstack/cocoon-agent/client"
)

func newClientCmd() *cobra.Command {
	var (
		cid  uint32
		port uint32
	)

	cmd := &cobra.Command{
		Use:                "client [flags] -- <argv>...",
		Short:              "Run a command on a remote cocoon-agent (debug / smoke test)",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			conn, err := dialVsock(cid, port)
			if err != nil {
				return fmt.Errorf("dial vsock cid=%d port=%d: %w", cid, port, err)
			}
			defer conn.Close() //nolint:errcheck

			exitCode, err := client.Run(ctx, conn, args, nil, os.Stdin, os.Stdout, os.Stderr)
			if err != nil {
				return err
			}
			os.Exit(exitCode)
			return nil
		},
	}
	cmd.Flags().Uint32Var(&cid, "cid", 0, "vsock CID of the target VM (host-side; from hypervisor)")
	cmd.Flags().Uint32Var(&port, "port", agent.DefaultPort, "vsock port the agent is listening on")
	_ = cmd.MarkFlagRequired("cid")
	return cmd
}
