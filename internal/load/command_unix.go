//go:build unix

package load

import (
	"os/exec"
	"syscall"
)

// inOwnGroup makes cmd lead a process group of its own, and its cancellation kill
// that whole group: a child the go command starts, or a wrapper's, would otherwise
// outlive it and hold its output open.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
