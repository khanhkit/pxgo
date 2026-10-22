package proxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/diagnostic"
)

// Experimental, internal-only rollout: default legacy preserves the shipped
// workers*threads admission contract until native platform benchmarks pass.
type capacityMode string

const (
	modeLegacy              capacityMode = "legacy"
	modeShadow              capacityMode = "shadow"
	modeAuto                capacityMode = "auto"
	capacityStateNormal                  = "normal"
	capacityStatePressure                = "pressure"
	capacityStateRecovering              = "recovering"
	capacityEmergencyFD                  = "emergency-fd"
)

func experimentalCapacityMode() (capacityMode, error) {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE")))
	switch capacityMode(value) {
	case "", modeLegacy:
		return modeLegacy, nil
	case modeShadow:
		return modeShadow, nil
	case modeAuto:
		return modeAuto, nil
	}
	return "", fmt.Errorf("invalid PXGO_EXPERIMENTAL_CAPACITY_MODE %q: expected legacy, shadow or auto", value)
}

// This conservative fallback mirrors the existing default budget during the
// opt-in trial; it is not a release-calibrated automatic capacity or user target.
const capacityColdStartWindow = 32

type capacitySample struct {
	available bool
	freeFD    int
	totalFD   int
	reason    string
}

type capacityGovernor struct {
	mu             sync.Mutex
	changed        chan struct{}
	mode           capacityMode
	manual         int
	active         int
	soft           int
	hard           int
	effective      int
	waits          uint64
	waiting        int
	timeouts       uint64
	state          string
	reason         string
	sensor         string
	lastTransition time.Time
	healthy        int
	pressured      int
	cooldown       time.Time
	lastSample     time.Time
	probeAllowed   bool // at most one post-cooldown new socket when sensors are unavailable
	closed         bool
}

func newCapacityGovernor(mode capacityMode, cfg config.Config) *capacityGovernor {
	if mode == modeLegacy {
		return nil
	}
	g := &capacityGovernor{changed: make(chan struct{}), mode: mode, manual: cfg.MaxConnections, soft: capacityColdStartWindow, hard: capacityColdStartWindow, state: capacityStateNormal, sensor: "cold-start", lastTransition: time.Now()}
	g.recomputeLocked()
	return g
}
func (g *capacityGovernor) notifyLocked() { close(g.changed); g.changed = make(chan struct{}) }
func (g *capacityGovernor) recomputeLocked() {
	n := g.soft
	if g.hard < n {
		n = g.hard
	}
	if g.manual > 0 && g.manual < n {
		n = g.manual
	}
	if g.state == capacityStatePressure && g.reason == capacityEmergencyFD {
		n = 0
		if g.probeAllowed {
			// An already established tunnel does not consume the bounded recovery
			// probe, which allows exactly one additional downstream socket.
			n = g.active + 1
			if g.manual > 0 && n > g.manual {
				n = g.manual
			}
		}
	}
	if n < 0 {
		n = 0
	}
	if g.effective != n {
		g.effective = n
		g.notifyLocked()
	}
}

func (g *capacityGovernor) release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
		g.notifyLocked()
	}
	g.mu.Unlock()
}

func (g *capacityGovernor) close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if !g.closed {
		g.closed = true
		g.notifyLocked()
	}
	g.mu.Unlock()
}

type capacityWaitTimeout struct{}

func (capacityWaitTimeout) Error() string   { return "capacity admission wait timed out" }
func (capacityWaitTimeout) Timeout() bool   { return true }
func (capacityWaitTimeout) Temporary() bool { return true }

var _ net.Error = capacityWaitTimeout{}

func (g *capacityGovernor) acquire(done <-chan struct{}, wait time.Duration) error {
	if wait <= 0 {
		return capacityWaitTimeout{}
	}
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	registered := false
	defer func() {
		if registered {
			g.mu.Lock()
			g.waiting--
			g.mu.Unlock()
		}
	}()
	for {
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return net.ErrClosed
		}
		limit := g.effective
		if g.mode == modeShadow {
			limit = 0
		} // shadow never acquires; legacy channel remains authoritative
		if g.active < limit {
			g.active++
			if g.probeAllowed {
				g.probeAllowed = false
				g.cooldown = g.lastSample.Add(3 * time.Second)
				g.recomputeLocked()
			}
			g.mu.Unlock()
			return nil
		}
		if !registered {
			g.waits++
			g.waiting++
			registered = true
			// No timer allocation on the successful uncontended hot path.
			timer = time.NewTimer(wait)
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
		case <-done:
			return net.ErrClosed
		case <-timer.C:
			g.mu.Lock()
			g.timeouts++
			g.mu.Unlock()
			return capacityWaitTimeout{}
		}
	}
}

// sample is deterministic for injected timestamps/sensor readings. Demand
// never causes shrink; unsupported or stale sensors cannot authorize growth.
func (g *capacityGovernor) sample(s capacitySample, now time.Time) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.lastSample = now
	if !s.available {
		g.sensor = "unsupported"
		g.healthy = 0
		g.pressured = 0
		if g.state == capacityStatePressure && g.reason == capacityEmergencyFD && !now.Before(g.cooldown) {
			// When FD instrumentation also fails during exhaustion, allow one
			// bounded recovery probe after cooldown, never an uncontrolled reopen.
			g.probeAllowed = true
		}
		g.recomputeLocked()
		return
	}
	g.sensor = "fresh"
	g.probeAllowed = false // fresh instrumentation supersedes an unverified probe
	// FD headroom is a conservative trial estimate (4 descriptors per new
	// CONNECT and 256 descriptors reserved for all other process traffic).
	const reserveFD = 256
	const fdPerConnection = 4
	available := s.totalFD - s.freeFD
	if s.freeFD < 0 || s.totalFD <= 0 || available < 0 || available > s.totalFD {
		g.sensor = "invalid"
		g.healthy = 0
		g.pressured = 0
		return
	}
	hard := g.active + max(0, (s.freeFD-reserveFD)/fdPerConnection)
	if hard < g.hard {
		g.hard = hard
	}
	if s.freeFD <= reserveFD {
		g.pressured++
		g.healthy = 0
		if g.pressured >= 2 && !now.Before(g.cooldown) {
			if g.state != capacityStatePressure {
				g.state = capacityStatePressure
				g.lastTransition = now
			}
			g.reason = "fd-headroom"
			g.soft = max(1, g.soft/2)
			g.cooldown = now.Add(3 * time.Second)
		}
	} else {
		g.healthy++
		g.pressured = 0
		if g.state == capacityStatePressure && g.healthy >= 3 {
			g.state = capacityStateRecovering
			g.lastTransition = now
			g.cooldown = now.Add(3 * time.Second)
		}
		if g.state == capacityStateRecovering && g.healthy >= 6 {
			g.state = capacityStateNormal
			g.lastTransition = now
		}
		if g.healthy >= 3 && !now.Before(g.cooldown) {
			if hard > g.hard {
				g.hard = hard
			}
			if g.state == capacityStateNormal && (g.active >= g.effective || g.waiting > 0) && g.soft < g.hard {
				g.soft += 8
				g.cooldown = now.Add(time.Second)
			}
		}
		if g.state == capacityStateNormal {
			g.reason = "none"
		}
	}
	g.recomputeLocked()
}

func (g *capacityGovernor) recordAcceptError(err error) {
	if g == nil || (!errors.Is(err, syscall.EMFILE) && !errors.Is(err, syscall.ENFILE)) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.state = capacityStatePressure
	g.reason = capacityEmergencyFD
	g.sensor = "critical"
	g.lastTransition = time.Now()
	g.healthy = 0
	g.cooldown = time.Now().Add(3 * time.Second)
	g.probeAllowed = false
	g.recomputeLocked()
}

func (g *capacityGovernor) snapshot() diagnostic.CapacitySnapshot {
	if g == nil {
		return diagnostic.CapacitySnapshot{Mode: string(modeLegacy), State: capacityStateNormal}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return diagnostic.CapacitySnapshot{Mode: string(g.mode), State: g.state, ActiveDownstream: g.active, SoftWindow: g.soft, HardEnvelope: g.hard, EffectiveLimit: g.effective, ManualCeiling: g.manual, ManualCeilingEnabled: g.manual > 0, PressureReason: g.reason, AdmissionWaits: g.waits, AdmissionTimeouts: g.timeouts, SensorAvailability: g.sensor, LastTransition: g.lastTransition}
}

func (s *Server) capacityDiagnosticSnapshot() diagnostic.CapacitySnapshot {
	if s.capacity != nil {
		return s.capacity.snapshot()
	}
	snap := diagnostic.CapacitySnapshot{Mode: string(modeLegacy), State: capacityStateNormal, SensorAvailability: "legacy"}
	if s.legacySlots != nil {
		snap.ActiveDownstream = len(s.legacySlots)
		snap.EffectiveLimit = cap(s.legacySlots)
		snap.HardEnvelope = snap.EffectiveLimit
		snap.SoftWindow = snap.EffectiveLimit
	}
	return snap
}

func (g *capacityGovernor) observeAdmission() {
	g.mu.Lock()
	g.active++
	g.mu.Unlock()
}
