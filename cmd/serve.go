package cmd

import (
	"fmt"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-agent/agent"
)

func newServeCmd() *cobra.Command {
	var port uint32

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Listen on vsock and serve exec requests",
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			logger := log.WithFunc("cmd.serve")

			listener, err := listenVsock(port)
			if err != nil {
				return fmt.Errorf("listen vsock port %d: %w", port, err)
			}

			srv := agent.NewServer(listener)
			logger.Infof(ctx, "cocoon-agent serving vsock port %d", port)
			return srv.Serve(ctx)
		},
	}
	cmd.Flags().Uint32Var(&port, "port", agent.DefaultPort, "vsock port to listen on")
	return cmd
}
