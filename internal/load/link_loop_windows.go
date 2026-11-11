//go:build windows

package load

import "syscall"

// errLinkLoop is the error asking through a symbolic link that loops gives:
// ERROR_CANT_RESOLVE_FILENAME.
const errLinkLoop = syscall.Errno(1921)
