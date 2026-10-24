//go:build windows

package proxy

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type (
	capacityIsolatedResourceSample struct {
		PrivateBytes, RSSBytes uint64
		Handles                uint32
		CPUNanos               uint64
	}
	capacityWindowsProcessMemoryCounters struct {
		Size                                                                                                                                     uint32
		PageFaults                                                                                                                               uint32
		PeakWorkingSet, WorkingSet, QuotaPeakPaged, QuotaPaged, QuotaPeakNonPaged, QuotaNonPaged, PageFileUsage, PeakPageFileUsage, PrivateUsage uintptr
	}
)

var capacityIsolatedPSAPI = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

func readCapacityIsolatedResources(pid int) (capacityIsolatedResourceSample, error) {
	var s capacityIsolatedResourceSample
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return s, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var m capacityWindowsProcessMemoryCounters
	m.Size = uint32(unsafe.Sizeof(m))
	ok, _, e := capacityIsolatedPSAPI.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.Size))
	if ok == 0 {
		return s, fmt.Errorf("GetProcessMemoryInfo: %v", e)
	}
	var handles uint32
	ok, _, e = capacityGetProcessHandleCount.Call(uintptr(h), uintptr(unsafe.Pointer(&handles)))
	if ok == 0 {
		return s, fmt.Errorf("GetProcessHandleCount: %v", e)
	}
	s.PrivateBytes = uint64(m.PrivateUsage)
	s.RSSBytes = uint64(m.WorkingSet)
	s.Handles = handles
	var creation, exit, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return s, fmt.Errorf("GetProcessTimes: %w", err)
	}
	s.CPUNanos = uint64(kernel.Nanoseconds() + user.Nanoseconds())
	return s, nil
}
