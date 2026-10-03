package memory

import (
	"encoding/binary"
	"syscall"
)

// PlatformSources reads the free pages the kernel can hand out at once, capped by
// the machine's physical memory.
func PlatformSources() []Source {
	return []Source{freePages}
}

// freePages is the free, speculative and purgeable pages times the page size: the
// pages the kernel gives a process without paging anything out.
func freePages() (uint64, bool) {
	pageSize, err := syscall.SysctlUint32("hw.pagesize")
	if err != nil || pageSize == 0 {
		return 0, false
	}
	var pages uint64
	for _, name := range []string{"vm.page_free_count", "vm.page_speculative_count", "vm.page_purgeable_count"} {
		count, err := syscall.SysctlUint32(name)
		if err != nil {
			return 0, false
		}
		pages += uint64(count)
	}
	room := pages * uint64(pageSize)
	if physical, ok := memsize(); ok {
		room = min(room, physical)
	}
	return room, true
}

// memsize reads hw.memsize. The string syscall.Sysctl returns drops a trailing
// zero byte, which a little-endian 64-bit value below 2^56 always ends with, so
// the bytes are padded back to eight before they are decoded.
func memsize() (uint64, bool) {
	raw, err := syscall.Sysctl("hw.memsize")
	if err != nil || len(raw) == 0 || len(raw) > 8 {
		return 0, false
	}
	var buf [8]byte
	copy(buf[:], raw)
	return binary.LittleEndian.Uint64(buf[:]), true
}
