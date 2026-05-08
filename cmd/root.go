// Package cmd wires cocoon-agent subcommands.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-agent/version"
)

// NewRootCmd returns the cocoon-agent root command with all subcommands wired.
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

// Execute is the binary entrypoint. On Windows under the SCM it dispatches
// into runAsWindowsService; otherwise it falls through to run().
func Execute() {
	isService, err := runAsWindowsService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "windows service dispatch: %v\n", err)
		os.Exit(1)
	}
	if isService {
		return
	}
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
		var ec *exitCodeError
		if errors.As(err, &ec) {
			return ec.code
		}
		log.WithFunc("cmd.Execute").Error(ctx, err, "command failed")
		return 1
	}
	return 0
}
