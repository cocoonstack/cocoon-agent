package cmd

import (
	"context"
	"os"

	"github.com/projecteru2/core/log"
	"github.com/projecteru2/core/types"
)

// setupLog reads AGENT_LOG_LEVEL (default info). File logging is off —
// systemd captures stdout/stderr to journald inside the VM.
func setupLog(ctx context.Context) error {
	level := os.Getenv("AGENT_LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	return log.SetupLog(ctx, &types.ServerLogConfig{Level: level}, "")
}
