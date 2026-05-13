package cmd

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-agent/agent"
)

const (
	listenRetryInterval = time.Second
	listenRetryTimeout  = 2 * time.Minute
)

func newServeCmd() *cobra.Command {
	var port uint32

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Listen on vsock and serve exec requests",
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			listener, err := listenVsockWithRetry(ctx, port)
			if err != nil {
				return fmt.Errorf("listen vsock port %d: %w", port, err)
			}
			srv := agent.NewServer(listener)
			log.WithFunc("cmd.serve").Infof(ctx, "cocoon-agent serving vsock port %d", port)
			return srv.Serve(ctx)
		},
	}
	cmd.Flags().Uint32Var(&port, "port", agent.DefaultPort, "vsock port to listen on")
	return cmd
}

// listenVsockWithRetry rides out the viosock PnP-bind window on Windows boot
// and snap/restore — Linux normally succeeds first try.
func listenVsockWithRetry(ctx context.Context, port uint32) (net.Listener, error) {
	logger := log.WithFunc("cmd.serve.listenRetry")
	deadline := time.Now().Add(listenRetryTimeout)
	for attempt := 1; ; attempt++ {
		lsn, err := listenVsock(ctx, port)
		if err == nil {
			if attempt > 1 {
				logger.Infof(ctx, "vsock listen succeeded on attempt %d", attempt)
			}
			return lsn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("after %d attempts within %s: %w", attempt, listenRetryTimeout, err)
		}
		if attempt == 1 || attempt%5 == 0 {
			logger.Warnf(ctx, "vsock listen attempt %d failed: %v (retrying for up to %s)", attempt, err, listenRetryTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(listenRetryInterval):
		}
	}
}
