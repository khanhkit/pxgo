package proxy

import (
	"fmt"
	"sync"
	"time"
)

// Phase-indexed independent-process sampler. It begins BEFORE the first CONNECT
// dial, keeps sampling through HTTP churn and draining, and records failures.
const (
	isolatedRamp  = 0
	isolatedMixed = 1
	isolatedDrain = 2
)

type capacityIsolatedSampler struct {
	pid       int
	mu        sync.Mutex
	phase     int
	peak      capacityIsolatedResourceSample
	phasePeak [3]capacityIsolatedResourceSample
	samples   [3]int
	firstErr  error
	stopped   sync.Once
	stop      chan struct{}
	done      chan struct{}
}

func updateIsolatedPeak(to *capacityIsolatedResourceSample, from capacityIsolatedResourceSample) {
	if from.PrivateBytes > to.PrivateBytes {
		to.PrivateBytes = from.PrivateBytes
	}
	if from.RSSBytes > to.RSSBytes {
		to.RSSBytes = from.RSSBytes
	}
	if from.Handles > to.Handles {
		to.Handles = from.Handles
	}
	if from.CPUNanos > to.CPUNanos {
		to.CPUNanos = from.CPUNanos
	}
}

func startCapacityIsolatedSampler(pid int, baseline capacityIsolatedResourceSample) *capacityIsolatedSampler {
	m := &capacityIsolatedSampler{pid: pid, peak: baseline, stop: make(chan struct{}), done: make(chan struct{})}
	m.capture() // before first dial; required sample for ramp
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.capture()
			}
		}
	}()
	return m
}

func (m *capacityIsolatedSampler) capture() {
	measured, err := readCapacityIsolatedResources(m.pid)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		if m.firstErr == nil {
			m.firstErr = fmt.Errorf("child-PID resource telemetry: %w", err)
		}
		return
	}
	updateIsolatedPeak(&m.peak, measured)
	updateIsolatedPeak(&m.phasePeak[m.phase], measured)
	m.samples[m.phase]++
}

func (m *capacityIsolatedSampler) setPhase(phase int) {
	m.mu.Lock()
	m.phase = phase
	m.mu.Unlock()
	m.capture() // guarantee at least one real measurement in every phase
}

func (m *capacityIsolatedSampler) stopSampling() (capacityIsolatedResourceSample, [3]capacityIsolatedResourceSample, [3]int, error) {
	m.stopped.Do(func() { close(m.stop) })
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak, m.phasePeak, m.samples, m.firstErr
}
