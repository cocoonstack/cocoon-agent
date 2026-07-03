//go:build linux

package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestDropStaleDBusMachineID(t *testing.T) {
	t.Run("regular file is removed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "machine-id")
		if err := os.WriteFile(path, []byte("oldid\n"), 0o444); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := dropStaleDBusMachineID(path); err != nil {
			t.Fatalf("dropStaleDBusMachineID: %v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("regular file still present (err=%v), want removed", err)
		}
	})

	t.Run("symlink is preserved", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "etc-machine-id")
		if err := os.WriteFile(target, []byte("id\n"), 0o444); err != nil {
			t.Fatalf("seed target: %v", err)
		}
		link := filepath.Join(dir, "machine-id")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := dropStaleDBusMachineID(link); err != nil {
			t.Fatalf("dropStaleDBusMachineID: %v", err)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink not preserved (fi=%v err=%v)", fi, err)
		}
	})

	t.Run("missing file is a no-op", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "does-not-exist")
		if err := dropStaleDBusMachineID(path); err != nil {
			t.Errorf("dropStaleDBusMachineID on missing file: %v, want nil", err)
		}
	})
}
