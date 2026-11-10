//go:build !windows

package load

import "syscall"

// errLinkLoop is the error asking through a symbolic link that loops gives.
const errLinkLoop = syscall.ELOOP
