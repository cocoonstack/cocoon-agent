// Package cmd wires the cocoon-agent subcommands.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-agent/version"
)

// NewRootCmd returns the root cobra command with subcommands attached.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "cocoon-agent",
		Short:   "vsock-based command exec agent for Cocoon-managed VMs",
		Version: fmt.Sprintf("%s (rev=%s built=%s)", version.VERSION, version.REVISION, version.BUILTAT),
	}
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newClientCmd())
	return rootCmd
}

// Execute runs the root command, exiting with the appropriate status. It
// is a thin wrapper around run() so deferred cancels and other cleanups
// run before the process exits.
func Execute() {
	os.Exit(run())
}

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := setupLog(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "setup log: %v\n", err)
		return 1
	}

	if err := NewRootCmd().ExecuteContext(ctx); err != nil {
		log.WithFunc("cmd.Execute").Errorf(ctx, err, "command failed")
		return 1
	}
	return 0
}
