//go:build windows

package load

import (
	"io/fs"
	"os"
	"syscall"
)

// idOf is the dirID of the directory at path: its volume serial number and file
// index ([fileInformation]).
func idOf(path string, _ fs.FileInfo) dirID {
	info, ok := fileInformation(path)
	if !ok {
		return resolvedID(path)
	}
	return dirID{dev: uint64(info.VolumeSerialNumber), ino: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}
}

// deviceOf is the serial number of the volume holding the entry at path, links and
// junctions followed ([fileInformation]), nil where it cannot be read.
func deviceOf(path string) any {
	info, ok := fileInformation(path)
	if !ok {
		return nil
	}
	return info.VolumeSerialNumber
}

// fileInformation is what the file system reports of the entry at path, links and
// junctions followed, which opening it with backup semantics reads without listing
// it, and whether it could be read.
func fileInformation(path string) (syscall.ByHandleFileInformation, bool) {
	var info syscall.ByHandleFileInformation
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return info, false
	}
	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	h, err := syscall.CreateFile(name, 0, share, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return info, false
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	return info, syscall.GetFileInformationByHandle(h, &info) == nil
}

// listedLink reports whether the entry at path, which a listing gave as a
// directory, is a link or a junction, which Lstat reports as irregular since Go
// 1.23.
func listedLink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
}
