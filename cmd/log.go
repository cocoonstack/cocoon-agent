package cmd

import (
	"cmp"
	"context"
	"os"

	"github.com/projecteru2/core/log"
	"github.com/projecteru2/core/types"
)

// File logging is off — systemd captures stdout/stderr to journald inside the VM.
func setupLog(ctx context.Context) error {
	level := cmp.Or(os.Getenv("AGENT_LOG_LEVEL"), "info")
	return log.SetupLog(ctx, &types.ServerLogConfig{Level: level}, "")
}
