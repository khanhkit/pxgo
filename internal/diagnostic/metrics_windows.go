//go:build windows

package diagnostic

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

func platformProcessMetrics() (float64, uint64, error) {
	handle := windows.CurrentProcess()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, 0, err
	}
	cpu := float64(kernel.Nanoseconds()+user.Nanoseconds()) / 1e9
	counters := processMemoryCounters{CB: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	r1, _, callErr := getProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.CB),
	)
	if r1 == 0 {
		return cpu, 0, fmt.Errorf("GetProcessMemoryInfo: %w", callErr)
	}
	return cpu, uint64(counters.WorkingSetSize), nil
}
