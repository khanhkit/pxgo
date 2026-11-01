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
	modeLegacy                   capacityMode = "legacy"
	modeShadow                   capacityMode = "shadow"
	modeAuto                     capacityMode = "auto"
	capacityStateNormal                       = "normal"
	capacityStatePressure                     = "pressure"
	capacityStateRecovering                   = "recovering"
	capacityEmergencyFD                       = "emergency-fd"
	capacityInvalidSensor                     = "invalid"
	capacitySensorColdStart                   = "cold-start"
	capacitySensorFresh                       = "fresh"
	capacityPressureCgroupMemory              = "cgroup-memory"
	capacityWindowsSensor                     = "windows-memory-handles"
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
	capacitySensorMaxAge    = 5 * time.Second // 20x the ordinary 250ms observation cadence
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
	available              bool
	freeFD                 int
	totalFD                int
	resource               string // empty on Linux; windows-memory-handles for experimental native envelope
	freeSlots              int
	resourcePressure       bool
	linuxMemoryConstrained bool // optional cgroup2 ceiling; never a second physical permit
	linuxMemoryFreeSlots   int
	linuxMemoryPressure    bool
	reason                 string
}

type capacityGovernor struct {
	mu                      sync.Mutex
	changed                 chan struct{}
	mode                    capacityMode
	manual                  int
	active                  int
	soft                    int
	hard                    int
	effective               int
	waits                   uint64
	waiting                 int
	nextWaitTicket          uint64
	waitQueue               []uint64 // FIFO only for accepted sockets awaiting one physical permit
	timeouts                uint64
	state                   string
	reason                  string
	sensor                  string
	lastTransition          time.Time
	healthy                 int
	pressured               int
	cooldown                time.Time
	lastSample              time.Time
	lastVerifiedReceipt     time.Time               // monotonic receipt, never synthetic sample/event timestamps
	sensorFreshnessBlocked  bool                    // if samples stop arriving, revoke NEW intake only
	probeAllowed            bool                    // at most one post-cooldown new socket when sensors are unavailable
	startupSensorUntrusted  bool                    // failed first runtime preflight cannot authorize AUTO 256 cold-start slots
	memoryAdmissionBlocked  bool                    // cgroup has no verified NEW physical memory headroom
	memorySampleConstrained bool                    // most recent valid Linux cgroup observation is finite
	memorySampleRemaining   int                     // fresh-new-socket permits; never replenished by old socket close
	memoryPendingSlots      int                     // reservations not yet reflected by memory.current
	memoryLastObservedSlots int                     // previous measured free slots for reservation debt reconciliation
	classes                 *connectionClassManager // opt-in accounting ONLY, same physical permit
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
	g := &capacityGovernor{changed: make(chan struct{}), mode: mode, manual: cfg.MaxConnections, soft: capacityColdStartWindow, hard: capacityColdStartWindow, state: capacityStateNormal, sensor: capacitySensorColdStart, lastTransition: time.Now()}
	if mode == modeAuto && experimentalConnectionClassesEnabled() {
		g.classes = newConnectionClassManager()
		g.classes.strictLongIntake = experimentalStrictLongIntakeEnabled()
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
	// A known-good FD-only budget must not authorize more TCP sockets after
	// its sensor disappears or reports contradictory values. Preserve the
	// existing single EMFILE/ENFILE recovery probe, but never an ordinary
	// admission from a stale hard envelope.
	emergencyProbe := g.state == capacityStatePressure && g.reason == capacityEmergencyFD && g.probeAllowed
	if g.sensor == capacityInvalidSensor || (g.sensor == "unsupported" && !emergencyProbe) {
		n = 0
	}
	if g.sensorFreshnessBlocked && (g.state != capacityStatePressure || g.reason != capacityEmergencyFD || !g.probeAllowed) {
		// A stalled sampler must not reuse the last valid OS headroom.
		// Keep the pre-existing single bounded EMFILE/ENFILE recovery probe.
		n = 0
	}
	if g.startupSensorUntrusted {
		// Server.Start() has attempted a preflight, but no valid sensor has
		// verified the provisional AUTO cold-start allowance yet.
		n = 0
	}
	if g.memoryAdmissionBlocked {
		// A released socket does not manufacture cgroup memory headroom.
		// Only the next valid positive memory measurement can reopen intake.
		n = 0
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

// Called only with g.mu held. No additional goroutine, timer or scheduler.
// A new connection (or diagnostic observer) detects a stalled OS sampler.
func (g *capacityGovernor) expireSensorFreshnessLocked() {
	if g.mode == modeAuto && !g.lastVerifiedReceipt.IsZero() &&
		time.Since(g.lastVerifiedReceipt) > capacitySensorMaxAge && !g.sensorFreshnessBlocked {
		g.sensorFreshnessBlocked = true
		// Keep diagnostics truthful: expired telemetry is not still fresh.
		// Preserve stronger explicit invalid/unavailable error classifications.
		if g.sensor == capacitySensorFresh || g.sensor == capacityWindowsSensor {
			g.sensor = "stale"
		}
		g.recomputeLocked()
	}
}

func (g *capacityGovernor) acquire(done <-chan struct{}, wait time.Duration) error {
	if wait <= 0 {
		return capacityWaitTimeout{}
	}
	var timer *time.Timer
	registered := false
	var ticket uint64
	defer func() {
		if timer != nil {
			timer.Stop()
		}
		if registered {
			g.mu.Lock()
			for i, waitingTicket := range g.waitQueue {
				if waitingTicket == ticket {
					g.waitQueue = append(g.waitQueue[:i], g.waitQueue[i+1:]...)
					if i == 0 {
						g.notifyLocked() // allow the next waiter to take a freed slot
					}
					break
				}
			}
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
		// Listener shutdown must win over available capacity, including
		// when a canceled waiter wakes at the same time as a release.
		select {
		case <-done:
			g.mu.Unlock()
			return net.ErrClosed
		default:
		}
		g.expireSensorFreshnessLocked()
		limit := g.effective
		if g.mode == modeShadow {
			limit = 0
		} // shadow never acquires; legacy channel remains authoritative
		// New arrivals must not steal a permit from an already waiting
		// listener. Only the head of the FIFO may proceed while queued.
		first := len(g.waitQueue) == 0 || (registered && g.waitQueue[0] == ticket)
		if g.active < limit && first {
			g.active++
			if g.memorySampleConstrained {
				g.memoryPendingSlots++ // track estimated reserved bytes across later unchanged samples
				// An observed free cgroup slot authorizes AT MOST one new
				// admission until another fresh sample. Closing an older
				// connection cannot regenerate this unmeasured allowance.
				g.memorySampleRemaining--
				if g.memorySampleRemaining <= 0 {
					g.memoryAdmissionBlocked = true
					g.recomputeLocked()
				}
			}
			if registered {
				g.waitQueue = g.waitQueue[1:]
				g.waiting--
				registered = false
				g.notifyLocked() // spare slots can immediately serve the next FIFO waiter
			}
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
			g.nextWaitTicket++
			ticket = g.nextWaitTicket
			g.waitQueue = append(g.waitQueue, ticket)
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
	wasColdStart := g.sensor == capacitySensorColdStart
	if !s.available {
		if g.sensor == capacitySensorColdStart {
			g.startupSensorUntrusted = true
		}
		// A previously observed finite cgroup quota cannot be trusted after
		// telemetry failure. Keep existing leases; deny NEW physical sockets
		// until another valid measurement establishes memory headroom.
		if g.memorySampleConstrained {
			g.memoryAdmissionBlocked = true
		}
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
	g.sensor = capacitySensorFresh
	g.probeAllowed = false // fresh instrumentation supersedes an unverified probe
	var hard int
	var pressured bool
	pressureReason := "fd-headroom"
	switch s.resource {
	case "": // Real Linux RLIMIT_NOFILE /proc/self/fd sensor
		// Reserve at most 256 FDs, scaling the margin down for low nofile.
		reserveFD := min(256, max(16, s.totalFD/2))
		const fdPerConnection = 4
		available := s.totalFD - s.freeFD
		if s.freeFD < 0 || s.totalFD <= 0 || available < 0 || available > s.totalFD {
			if wasColdStart {
				g.startupSensorUntrusted = true
			}
			if g.memorySampleConstrained {
				g.memoryAdmissionBlocked = true
			}
			g.sensor = capacityInvalidSensor
			g.healthy = 0
			g.pressured = 0
			g.recomputeLocked() // withdraw any outstanding recovery probe
			return
		}
		hard = g.active + max(0, (s.freeFD-reserveFD)/fdPerConnection)
		pressured = s.freeFD <= reserveFD
		if s.linuxMemoryConstrained {
			// Memory and FD are independent constraints on ONE global physical
			// permit. Never add the budgets or reduce active held connections.
			if s.linuxMemoryFreeSlots < 0 {
				if wasColdStart {
					g.startupSensorUntrusted = true
				}
				if g.memorySampleConstrained {
					g.memoryAdmissionBlocked = true
				}
				g.sensor = capacityInvalidSensor
				g.healthy = 0
				g.pressured = 0
				g.recomputeLocked()
				return
			}
			// Account for new sockets whose provisional 1MiB reservation is
			// not yet reflected in a changed kernel memory.current reading.
			// Unchanged positive telemetry is NOT a fresh allocation of
			// another full batch of virtual memory permits.
			if g.memorySampleConstrained {
				observedReduction := g.memoryLastObservedSlots - s.linuxMemoryFreeSlots
				if observedReduction > 0 {
					g.memoryPendingSlots = max(0, g.memoryPendingSlots-observedReduction)
				}
			} else {
				g.memoryPendingSlots = 0
			}
			g.memoryLastObservedSlots = s.linuxMemoryFreeSlots
			g.memorySampleRemaining = max(0, s.linuxMemoryFreeSlots-g.memoryPendingSlots)
			hard = min(hard, g.active+g.memorySampleRemaining)
		} else {
			if g.memorySampleConstrained {
				// A previously verified finite cgroup controller cannot vanish
				// and silently downgrade AUTO to FD-only admission mid-run.
				// Without trusted provenance for the hierarchy transition,
				// keep existing connections and await another finite sample.
				g.memoryAdmissionBlocked = true
				g.sensor = capacityInvalidSensor
				g.healthy = 0
				g.pressured = 0
				g.recomputeLocked()
				return
			}
			g.memoryPendingSlots = 0
			g.memoryLastObservedSlots = 0
			g.memorySampleRemaining = 0
		}
		g.memorySampleConstrained = s.linuxMemoryConstrained
		g.memoryAdmissionBlocked = s.linuxMemoryConstrained && g.memorySampleRemaining == 0
		if s.linuxMemoryPressure {
			pressureReason = capacityPressureCgroupMemory
			pressured = true
		}
	case capacityWindowsSensor:
		// Windows handle counts have no portable RLIMIT analogue. Use only
		// the separately bounded native memory+handle headroom estimate.
		if s.freeSlots < 0 || s.freeSlots > 4096 {
			if wasColdStart {
				g.startupSensorUntrusted = true
			}
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
		if pressured {
			pressureReason = capacityWindowsSensor
		}
		g.sensor = capacityWindowsSensor
	default:
		if wasColdStart {
			g.startupSensorUntrusted = true
		}
		if g.memorySampleConstrained {
			// Unknown drivers cannot inherit a previously finite cgroup
			// allowance without presenting validated resource telemetry.
			g.memoryAdmissionBlocked = true
		}
		g.sensor = capacityInvalidSensor
		g.healthy = 0
		g.pressured = 0
		g.recomputeLocked()
		return
	}
	// Timestamp a trusted *receipt*, not the (possibly synthetic) event
	// timestamp. The monotonic component detects a stalled sampling loop.
	g.lastVerifiedReceipt = time.Now()
	g.sensorFreshnessBlocked = false
	// Only a validated OS measurement can authorize AUTO cold-start intake.
	g.startupSensorUntrusted = false
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
			g.reason = pressureReason
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
		g.growWindowIfReadyLocked(now)
		if g.state == capacityStateNormal {
			g.reason = "none"
		}
	}
	g.recomputeLocked()
}

func (g *capacityGovernor) growWindowIfReadyLocked(now time.Time) {
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
	g.expireSensorFreshnessLocked()
	snapshot := diagnostic.CapacitySnapshot{Mode: string(g.mode), State: g.state, ActiveDownstream: g.active, SoftWindow: g.soft, HardEnvelope: g.hard, EffectiveLimit: g.effective, ManualCeiling: g.manual, ManualCeilingEnabled: g.manual > 0, PressureReason: g.reason, AdmissionWaits: g.waits, AdmissionTimeouts: g.timeouts, SensorAvailability: g.sensor, LastTransition: g.lastTransition}
	if g.classes != nil {
		c := g.classes.snapshot()
		snapshot.ClassPools = &diagnostic.CapacityClassSnapshot{Provisional: c.Provisional, Short: c.Short, Long: c.Long, ShortSoftBudget: c.ShortBudget, LongSoftBudget: c.LongBudget, Promoted: c.Promoted, LongOverflow: c.LongOverflow, LongIntakeDenied: c.LongDenied, LongDebt: c.LongDebt, Total: c.Total}
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
