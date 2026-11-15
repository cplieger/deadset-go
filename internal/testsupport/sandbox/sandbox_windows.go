//go:build windows

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
)

// Junction makes link inside a root a directory junction to target, which may
// name anything, through cmd's mklink run in a process group of its own: Windows
// offers no call making one short of writing its reparse data.
func Junction(ctx context.Context, target, link string) error {
	if err := inside(link, false); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "cmd", "/c", "mklink", "/J", link, target)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sandbox: mklink /J %s %s: %w: %s", link, target, err, out)
	}
	return nil
}
