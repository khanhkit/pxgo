//go:build !windows && !linux

package proxy

import "errors"

type capacityIsolatedResourceSample struct {
	PrivateBytes, RSSBytes uint64
	Handles                uint32
	CPUNanos               uint64
}

func readCapacityIsolatedResources(int) (capacityIsolatedResourceSample, error) {
	return capacityIsolatedResourceSample{}, errors.New("process-isolated benchmark not supported on this platform")
}
