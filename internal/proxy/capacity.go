package proxy

import (
	"errors"
	"fmt"
	"math"
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
	capacityInvalidSensor                = "invalid"
	capacityWindowsSensor                = "windows-memory-handles"
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

const (
	capacityControlInterval = 250 * time.Millisecond
	capacityGrowthCooldown  = 500 * time.Millisecond
)

// Owner-requested provisional startup target for opt-in AUTO. The Linux
// preflight sample can reduce the effective limit below this target when the
// real per-process FD budget cannot safely support 256 downstream sockets.
const (
	capacityColdStartWindow    = 256
	researchPolicyPID          = "pid"
	researchPolicyProportional = "proportional"
)

type capacitySample struct {
	available        bool
	freeFD           int
	totalFD          int
	resource         string // empty on Linux; windows-memory-handles for experimental native envelope
	freeSlots        int
	resourcePressure bool
	reason           string
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
	probeAllowed   bool                    // at most one post-cooldown new socket when sensors are unavailable
	classes        *connectionClassManager // opt-in accounting ONLY, same physical permit
	// Research-only: synthetic PI/PID alternatives; NOT shipped or proposed
	// for release, isolated experiment policy gated by PXGO_RESEARCH_CAPACITY_POLICY.
	researchIntegral     float64
	researchDeriv        float64
	researchPrevMeasured float64
	closed               bool
}

func newCapacityGovernor(mode capacityMode, cfg config.Config) *capacityGovernor {
	if mode == modeLegacy {
		return nil
	}
	g := &capacityGovernor{changed: make(chan struct{}), mode: mode, manual: cfg.MaxConnections, soft: capacityColdStartWindow, hard: capacityColdStartWindow, state: capacityStateNormal, sensor: "cold-start", lastTransition: time.Now()}
	if mode == modeAuto && experimentalConnectionClassesEnabled() {
		g.classes = newConnectionClassManager()
	}
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
		if g.probeAllowed && g.active < g.soft && g.active < g.hard && (g.manual == 0 || g.active < g.manual) {
			// Permit one probe only WITHIN the already established soft and hard
			// envelopes; a sensor outage never invents additional headroom.
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
		// Classes are advisory accounting only. Use the actual global
		// physical admission limit (including a manual/OS shrink), not
		// the stale soft target from server startup.
		if g.classes != nil {
			g.classes.sampleBudgets(n)
		}
		g.notifyLocked()
	}
}

func (g *capacityGovernor) release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
		if g.probeAllowed {
			g.recomputeLocked()
		}
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
	var hard int
	var pressured bool
	switch s.resource {
	case "": // Real Linux RLIMIT_NOFILE /proc/self/fd sensor
		// Reserve at most 256 FDs, scaling the margin down for low nofile.
		reserveFD := min(256, max(16, s.totalFD/2))
		const fdPerConnection = 4
		available := s.totalFD - s.freeFD
		if s.freeFD < 0 || s.totalFD <= 0 || available < 0 || available > s.totalFD {
			g.sensor = capacityInvalidSensor
			g.healthy = 0
			g.pressured = 0
			g.recomputeLocked() // withdraw any outstanding recovery probe
			return
		}
		hard = g.active + max(0, (s.freeFD-reserveFD)/fdPerConnection)
		pressured = s.freeFD <= reserveFD
	case capacityWindowsSensor:
		// Windows handle counts have no portable RLIMIT analogue. Use only
		// the separately bounded native memory+handle headroom estimate.
		if s.freeSlots < 0 || s.freeSlots > 4096 {
			g.sensor = capacityInvalidSensor
			g.healthy = 0
			g.pressured = 0
			g.recomputeLocked()
			return
		}
		// The provisional 4,096 limit is an absolute downstream trial cap,
		// not an additional 4,096 admissions on top of already active tunnels.
		// On a future lowered cap, retain useful existing connections, but
		// forbid any further admission until active is below the ceiling.
		const windowsPreviewMaxActive = 4096
		if g.active >= windowsPreviewMaxActive {
			hard = g.active
		} else {
			hard = g.active + min(s.freeSlots, windowsPreviewMaxActive-g.active)
		}
		pressured = s.resourcePressure
		g.sensor = capacityWindowsSensor
	default:
		g.sensor = capacityInvalidSensor
		g.healthy = 0
		g.pressured = 0
		g.recomputeLocked()
		return
	}
	if hard < g.hard {
		g.hard = hard
	}
	if pressured {
		g.pressured++
		g.healthy = 0
		if g.pressured >= 2 && !now.Before(g.cooldown) {
			if g.state != capacityStatePressure {
				g.state = capacityStatePressure
				g.lastTransition = now
			}
			g.reason = "fd-headroom"
			g.soft = max(1, g.soft/2)
			g.researchIntegral = 0
			g.researchDeriv = 0
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
		// A valid OS measurement can raise the hard envelope immediately; the
		// demand-controlled soft window still waits for consecutive healthy
		// samples to avoid growing on a transient sensor reading.
		if hard > g.hard {
			g.hard = hard
		}
		if g.healthy >= 3 && !now.Before(g.cooldown) {
			if g.state == capacityStateNormal && (g.active >= g.effective || g.waiting > 0) && g.soft < g.hard {
				// Fast additive/multiplicative increase under verified FD headroom.
				// No goroutine scheduler or per-route limits: the server-scoped
				// downstream gate remains the single authority.
				step := max(32, min(g.soft/2, 512))
				// RESEARCH MODE ONLY: leave R11 unchanged without an explicit
				// local experiment env. Run an identical real 2,560-CONNECT test
				// against PI/PID/HPA policies in this isolated lab checkout.
				switch os.Getenv("PXGO_RESEARCH_CAPACITY_POLICY") {
				case "fast_saturation":
					// Lab-only faster multiplicative ramp, still clipped by true
					// resource hard ceiling and all original overload safety gates.
					step = max(64, min(g.soft, 1024))
				case "pi", researchPolicyPID, researchPolicyProportional:
					backlog := float64(g.waiting)
					if backlog <= 0 {
						backlog = float64(max(0, g.active-g.effective+1))
					}
					// Integral is conditional: while saturating, freeze it;
					// derivative is on measured active sockets to avoid setpoint kick.
					dMeasured := (float64(g.active) - g.researchPrevMeasured) / .5
					g.researchDeriv = .75*g.researchDeriv + .25*dMeasured
					g.researchPrevMeasured = float64(g.active)
					kp, ki, kd := .55, .02, 0.
					policy := os.Getenv("PXGO_RESEARCH_CAPACITY_POLICY")
					if policy == researchPolicyPID {
						kp = .55
						ki = .02
						kd = .12
					}
					if policy == researchPolicyProportional {
						kp = .55
						ki = 0
						kd = 0
					}
					unclipped := kp*backlog + ki*g.researchIntegral - kd*g.researchDeriv
					if policy == researchPolicyPID && unclipped < 0 {
						unclipped = 0
					}
					step = max(32, min(512, int(math.Round(unclipped))))
					if step < 512 && g.soft+step < g.hard {
						g.researchIntegral = math.Min(3000, g.researchIntegral+backlog*.5)
					}
				case "hpa":
					// Demand-ratio controller with work-conserving, bounded slew.
					step = max(32, min(512, g.active+g.waiting-g.soft))
				}
				g.soft += min(step, g.hard-g.soft)
				g.cooldown = now.Add(capacityGrowthCooldown)
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
	snapshot := diagnostic.CapacitySnapshot{Mode: string(g.mode), State: g.state, ActiveDownstream: g.active, SoftWindow: g.soft, HardEnvelope: g.hard, EffectiveLimit: g.effective, ManualCeiling: g.manual, ManualCeilingEnabled: g.manual > 0, PressureReason: g.reason, AdmissionWaits: g.waits, AdmissionTimeouts: g.timeouts, SensorAvailability: g.sensor, LastTransition: g.lastTransition}
	if g.classes != nil {
		c := g.classes.snapshot()
		snapshot.ClassPools = &diagnostic.CapacityClassSnapshot{Provisional: c.Provisional, Short: c.Short, Long: c.Long, ShortSoftBudget: c.ShortBudget, LongSoftBudget: c.LongBudget, Promoted: c.Promoted, LongOverflow: c.LongOverflow, LongDebt: c.LongDebt, Total: c.Total}
	}
	return snapshot
}

func (s *Server) capacityDiagnosticSnapshot() diagnostic.CapacitySnapshot {
	if s.capacity != nil {
		return s.capacity.snapshot()
	}
	snap := diagnostic.CapacitySnapshot{Mode: string(modeLegacy), State: capacityStateNormal, SensorAvailability: string(modeLegacy)}
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
