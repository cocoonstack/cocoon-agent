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
	"unsafe"

	"github.com/projecteru2/core/log"
	"golang.org/x/sys/unix"
)

const (
	maxReseedEntropyBytes = 512
	machineIDBytes        = 16

	urandomPath       = "/dev/urandom"
	systemdRandomSeed = "/var/lib/systemd/random-seed"
	machineIDPath     = "/etc/machine-id"
	dbusMachineIDPath = "/var/lib/dbus/machine-id"
)

// runReseed injects host-fed entropy and forces a CRNG reseed so clones don't
// share the snapshot's CRNG state; steps are best-effort, errors joined.
func runReseed(ctx context.Context, req Message, enc *Encoder) error {
	var errs []error

	if err := reseedURandom(req.Data); err != nil {
		errs = append(errs, err)
	}
	clear(req.Data) // host entropy is single-use; don't leave it on the heap
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
	defer clear(buf) // the payload copy is single-use; zero it after the mix
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

// regenMachineID overwrites /etc/machine-id with a fresh random id so clones of
// one snapshot don't share it. systemd-machine-id-setup is deliberately avoided:
// in a VM it derives the id from the SMBIOS product_uuid, which a snapshot clone
// inherits verbatim — every clone would regenerate the same id. Skips silently
// when the file is absent (e.g. Android).
func regenMachineID(ctx context.Context) error {
	if _, err := os.Stat(machineIDPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", machineIDPath, err)
	}
	if err := writeRandomMachineID(); err != nil {
		return err
	}
	// Best-effort: /etc/machine-id is already fresh, but a surviving D-Bus copy
	// keeps serving the old id to dbus consumers — worth a warning.
	if err := dropStaleDBusMachineID(dbusMachineIDPath); err != nil {
		log.WithFunc("agent.regenMachineID").Warnf(ctx, "drop stale dbus machine id: %v", err)
	}
	return nil
}

// dropStaleDBusMachineID removes a baked regular-file D-Bus copy that would
// otherwise pin the old id; a symlink or missing file already tracks
// /etc/machine-id and is left alone.
func dropStaleDBusMachineID(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("lstat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func writeRandomMachineID() error {
	id, err := randomMachineID()
	if err != nil {
		return err
	}
	if err := os.WriteFile(machineIDPath, []byte(id), 0o444); err != nil { //nolint:gosec // matches /etc/machine-id's own world-readable convention
		return fmt.Errorf("write %s: %w", machineIDPath, err)
	}
	return nil
}

// randomMachineID returns a fresh id in /etc/machine-id's canonical
// 32-hex-lowercase + newline format.
func randomMachineID() (string, error) {
	buf := make([]byte, machineIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate machine id: %w", err)
	}
	return hex.EncodeToString(buf) + "\n", nil
}
