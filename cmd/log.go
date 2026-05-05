package cmd

import (
	"context"
	"os"

	"github.com/projecteru2/core/log"
	coretypes "github.com/projecteru2/core/types"
)

// setupLog initializes the projecteru2 logger from the AGENT_LOG_LEVEL env
// var (default info). cocoon-agent runs inside a VM where systemd captures
// stdout/stderr to journald, so file logging is intentionally disabled.
func setupLog(ctx context.Context) error {
	level := os.Getenv("AGENT_LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	return log.SetupLog(ctx, &coretypes.ServerLogConfig{Level: level}, "")
}
