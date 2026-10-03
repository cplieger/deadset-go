package memory

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	globalMemoryStatusEx      = kernel32.NewProc("GlobalMemoryStatusEx")
	queryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
	getProcessMemoryInfo      = kernel32.NewProc("K32GetProcessMemoryInfo")
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitProcessMemory       = 0x100
	jobObjectLimitJobMemory           = 0x200
)

// PlatformSources reads the limit of the job object holding this process, then the
// physical memory available to the machine.
func PlatformSources() []Source {
	return []Source{jobLimit, availablePhysical}
}

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

func availablePhysical() (uint64, bool) {
	status := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if ok, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); ok == 0 {
		return 0, false
	}
	return status.availPhys, true
}

// extendedLimitInformation is JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type extendedLimitInformation struct {
	perProcessUserTimeLimit int64
	perJobUserTimeLimit     int64
	limitFlags              uint32
	minimumWorkingSetSize   uintptr
	maximumWorkingSetSize   uintptr
	activeProcessLimit      uint32
	affinity                uintptr
	priorityClass           uint32
	schedulingClass         uint32
	ioCounters              [6]uint64
	processMemoryLimit      uintptr
	jobMemoryLimit          uintptr
	peakProcessMemoryUsed   uintptr
	peakJobMemoryUsed       uintptr
}

// processMemoryCountersEx is PROCESS_MEMORY_COUNTERS_EX.
type processMemoryCountersEx struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
	privateUsage               uintptr
}

// jobLimit is the tighter of the job object's process and job memory limits less
// the memory this process has committed, and false outside a job that sets one.
// A job's limit counts every process in it; the committed memory of the others is
// not read, so a job holding several processes reads more headroom than it has.
func jobLimit() (uint64, bool) {
	var info extendedLimitInformation
	ok, _, _ := queryInformationJobObject.Call(0, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0)
	if ok == 0 {
		return 0, false
	}
	var limit uint64
	limited := false
	if info.limitFlags&jobObjectLimitProcessMemory != 0 {
		limit, limited = uint64(info.processMemoryLimit), true
	}
	if info.limitFlags&jobObjectLimitJobMemory != 0 && (!limited || uint64(info.jobMemoryLimit) < limit) {
		limit, limited = uint64(info.jobMemoryLimit), true
	}
	if !limited {
		return 0, false
	}
	counters := processMemoryCountersEx{cb: uint32(unsafe.Sizeof(processMemoryCountersEx{}))}
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, false
	}
	if ok, _, _ := getProcessMemoryInfo.Call(uintptr(process), uintptr(unsafe.Pointer(&counters)), uintptr(counters.cb)); ok == 0 {
		return 0, false
	}
	used := uint64(counters.privateUsage)
	if used >= limit {
		return 0, true
	}
	return limit - used, true
}
