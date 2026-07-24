//go:build windows

package cmd

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sys/windows/svc"
)

const serviceName = "cocoon-agent"

type winService struct{}

// SCM stop/shutdown maps to ctx cancel, mirroring the SIGTERM path on POSIX.
func (s *winService) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (ssec bool, errno uint32) {
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := setupLog(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "setup log: %v\n", err)
		return false, 1
	}

	done := make(chan error, 1)
	go func() { done <- NewRootCmd().ExecuteContext(ctx) }()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				cancel()
				<-done
				status <- svc.Status{State: svc.StopPending}
				return false, 0
			}
		case err := <-done:
			status <- svc.Status{State: svc.StopPending}
			if err != nil {
				return false, 1
			}
			return false, 0
		}
	}
}

// A true result means SCM mode took over and the caller must return without further work.
func runAsWindowsService() (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("detect windows service: %w", err)
	}
	if !isService {
		return false, nil
	}
	return true, svc.Run(serviceName, &winService{})
}
