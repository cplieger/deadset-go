//go:build unix

package sandbox

import (
	"context"
	"net"
	"syscall"
	"testing"
)

// Mkfifo is [syscall.Mkfifo] inside a root.
func Mkfifo(path string, mode uint32) error {
	if err := inside(path, false); err != nil {
		return err
	}
	return syscall.Mkfifo(path, mode)
}

// Mknod is [syscall.Mknod] inside a root.
func Mknod(path string, mode uint32, dev int) error {
	if err := inside(path, false); err != nil {
		return err
	}
	return syscall.Mknod(path, mode, dev)
}

// ListenUnix is a Unix socket listener at path inside a root.
func ListenUnix(path string) (net.Listener, error) {
	if err := inside(path, false); err != nil {
		return nil, err
	}
	return (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
}

// Fifo makes a named pipe at path inside a root, skipping t where the host makes
// none, and when t ends opens it for writing, so no reader stays blocked on it, and
// removes it.
func Fifo(t *testing.T, path string) {
	t.Helper()
	if err := inside(path, false); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("Setup: this host makes no named pipe: %v", err)
	}
	t.Cleanup(func() {
		Unblock(path)
		_ = syscall.Unlink(path)
	})
}

// Unblock opens the named pipe at path for writing without waiting and closes it
// again, which lets every reader blocked opening it go on and read nothing. A pipe
// no reader holds open refuses the writer, and then nothing waits on it.
func Unblock(path string) {
	if fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err == nil {
		_ = syscall.Close(fd)
	}
}
