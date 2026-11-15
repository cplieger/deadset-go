//go:build !unix

package load

import "os/exec"

// inOwnGroup leaves cmd's cancellation killing the process: this platform names no
// process group a signal reaches, and [commandWaitDelay] bounds the wait for what
// a child holds open.
func inOwnGroup(*exec.Cmd) {}
