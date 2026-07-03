//go:build !linux

package agent

import (
	"context"
	"errors"
	"fmt"
)

// Reseed needs Linux-only RNG ioctls; other GOOSes answer with a sentinel error.
var errReseedUnsupported = errors.New("reseed is not supported on this OS")

func runReseed(_ context.Context, _ Message, enc *Encoder) error {
	if err := enc.SendErrorf("%v", errReseedUnsupported); err != nil {
		return fmt.Errorf("send reseed error frame: %w", err)
	}
	return errReseedUnsupported
}
