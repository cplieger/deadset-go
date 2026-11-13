//go:build windows

package load

import (
	"io/fs"
	"syscall"
)

// idOf is the dirID of the directory at path: its volume serial number and file
// index, which opening it with backup semantics reads without listing it.
func idOf(path string, _ fs.FileInfo) dirID {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return resolvedID(path)
	}
	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	h, err := syscall.CreateFile(name, 0, share, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return resolvedID(path)
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return resolvedID(path)
	}
	return dirID{dev: uint64(info.VolumeSerialNumber), ino: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}
}
