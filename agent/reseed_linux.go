//go:build linux

package agent

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	maxReseedEntropyBytes = 512
	machineIDBytes        = 16

	urandomPath       = "/dev/urandom"
	systemdRandomSeed = "/var/lib/systemd/random-seed"
	machineIDPath     = "/etc/machine-id"
)

// runReseed injects host-fed entropy and forces a CRNG reseed so clones don't
// share the snapshot's CRNG state; steps are best-effort, errors joined.
func runReseed(ctx context.Context, req Message, enc *Encoder) error {
	var errs []error

	if err := reseedURandom(req.Data); err != nil {
		errs = append(errs, err)
	}
	if err := os.Remove(systemdRandomSeed); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove systemd random seed: %w", err))
	}

	if req.RegenMachineID {
		if err := regenMachineID(ctx); err != nil {
			errs = append(errs, fmt.Errorf("regen machine id: %w", err))
		}
	}

	joined := errors.Join(errs...)
	if joined != nil {
		if err := enc.SendErrorf("reseed: %v", joined); err != nil {
			return fmt.Errorf("send reseed error frame: %w", err)
		}
		return joined
	}
	return enc.Encode(Message{Type: MsgExit, ExitCode: 0})
}

// reseedURandom opens /dev/urandom once and runs both entropy injection and
// CRNG reseed on the same fd, matching the kernel's expected ioctl sequence.
func reseedURandom(data []byte) error {
	fd, err := unix.Open(urandomPath, unix.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", urandomPath, err)
	}

	var errs []error
	if len(data) > 0 {
		if err := addEntropy(fd, data); err != nil {
			errs = append(errs, fmt.Errorf("add entropy: %w", err))
		}
	}
	if err := reseedCRNG(fd); err != nil {
		errs = append(errs, fmt.Errorf("reseed crng: %w", err))
	}
	if err := unix.Close(fd); err != nil {
		errs = append(errs, fmt.Errorf("close %s: %w", urandomPath, err))
	}
	return errors.Join(errs...)
}

// addEntropy injects data into the kernel entropy pool via RNDADDENTROPY.
// x/sys/unix exports no rand_pool_info helper, so the ioctl buffer
// (entropy_count bits, buf_size bytes, then the payload) is built by hand.
func addEntropy(fd int, data []byte) error {
	if len(data) > maxReseedEntropyBytes {
		data = data[:maxReseedEntropyBytes]
	}
	entropyBits := uint32(len(data)) * 8 //nolint:gosec // len(data) capped at maxReseedEntropyBytes above
	bufSize := uint32(len(data))         //nolint:gosec // len(data) capped at maxReseedEntropyBytes above

	buf := make([]byte, 8+len(data))
	binary.NativeEndian.PutUint32(buf[0:4], entropyBits)
	binary.NativeEndian.PutUint32(buf[4:8], bufSize)
	copy(buf[8:], data)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.RNDADDENTROPY, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 { //nolint:gosec // ioctl requires a raw pointer to the hand-built rand_pool_info buffer
		return fmt.Errorf("ioctl RNDADDENTROPY: %w", errno)
	}
	return nil
}

func reseedCRNG(fd int) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.RNDRESEEDCRNG, 0); errno != 0 {
		return fmt.Errorf("ioctl RNDRESEEDCRNG: %w", errno)
	}
	return nil
}

// regenMachineID truncates /etc/machine-id and regenerates it, skipping
// silently on systems that don't have the file (e.g. Android).
func regenMachineID(ctx context.Context) error {
	if _, err := os.Stat(machineIDPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", machineIDPath, err)
	}
	if err := os.Truncate(machineIDPath, 0); err != nil {
		return fmt.Errorf("truncate %s: %w", machineIDPath, err)
	}
	if path, err := exec.LookPath("systemd-machine-id-setup"); err == nil {
		if err := exec.CommandContext(ctx, path).Run(); err != nil { //nolint:gosec // path resolved by LookPath for a fixed binary name, not user input
			return fmt.Errorf("run systemd-machine-id-setup: %w", err)
		}
		return nil
	}
	return writeRandomMachineID()
}

// writeRandomMachineID is the fallback when systemd-machine-id-setup is
// unavailable: a fresh random id, matching /etc/machine-id's own format.
func writeRandomMachineID() error {
	buf := make([]byte, machineIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("generate machine id: %w", err)
	}
	line := hex.EncodeToString(buf) + "\n"
	if err := os.WriteFile(machineIDPath, []byte(line), 0o444); err != nil { //nolint:gosec // matches /etc/machine-id's own world-readable convention
		return fmt.Errorf("write %s: %w", machineIDPath, err)
	}
	return nil
}
