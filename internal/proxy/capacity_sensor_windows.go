//go:build windows

package proxy

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Preview only: Windows has no RLIMIT_NOFILE. A native handle sample and
// independently observed free physical+commit memory bound opt-in AUTO;
// these constants are not production/Job Object calibrated limits.
var (
	capacityKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	capacityGetProcessHandleCount = capacityKernel32.NewProc("GetProcessHandleCount")
	capacityGlobalMemoryStatusEx  = capacityKernel32.NewProc("GlobalMemoryStatusEx")
)

type capacityWindowsMemory struct {
	length            uint32
	load              uint32
	totalPhys         uint64
	availPhys         uint64
	totalPageFile     uint64
	availPageFile     uint64
	totalVirtual      uint64
	availVirtual      uint64
	availExtendedVirt uint64
}

func readCapacitySample() capacitySample {
	process, err := windows.GetCurrentProcess()
	if err != nil {
		return capacitySample{reason: "windows-process-handle-unavailable"}
	}
	var handles uint32
	ok, _, _ := capacityGetProcessHandleCount.Call(uintptr(process), uintptr(unsafe.Pointer(&handles)))
	if ok == 0 {
		return capacitySample{reason: "windows-native-handles-unavailable"}
	}
	mem := capacityWindowsMemory{length: uint32(unsafe.Sizeof(capacityWindowsMemory{}))}
	ok, _, _ = capacityGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem)))
	if ok == 0 || mem.totalPhys == 0 || mem.totalPageFile == 0 || mem.load > 100 {
		return capacitySample{reason: "windows-native-memory-unavailable"}
	}

	// Conservative trial estimates, NOT real OS handle/Job Object quotas:
	// reserve >=1GiB (or 1/8 host memory) for unrelated processes; reserve
	// 1MiB per extra downstream socket, and 4 process handles per socket.
	const (
		bytesPerConnection   = uint64(1 << 20)
		handlesPerConnection = uint64(4)
		previewHandleCeiling = uint64(32768)
		previewWindowCeiling = uint64(4096)
	)
	reserve := max(uint64(1<<30), mem.totalPhys/8)
	available := min(mem.availPhys, mem.availPageFile)
	var byMemory uint64
	if available > reserve {
		byMemory = (available - reserve) / bytesPerConnection
	}
	var byHandles uint64
	if uint64(handles) < previewHandleCeiling {
		byHandles = (previewHandleCeiling - uint64(handles)) / handlesPerConnection
	}
	freeSlots := min(byMemory, byHandles, previewWindowCeiling)
	return capacitySample{
		available: true, resource: capacityWindowsSensor,
		freeSlots: int(freeSlots), resourcePressure: freeSlots < 32,
		reason: fmt.Sprintf("native Windows handles=%d memory_load=%d%%", handles, mem.load),
	}
}
