package supervisor

import (
	"context"
	"sync"
	"time"
)

const (
	networkGapThreshold        = 5 * time.Second
	correlatedFailureWindow    = 3 * time.Second
	correlatedFailureThreshold = 8
	networkEpochCooldown       = 30 * time.Second
	internalFailureWindow      = 30 * time.Second
	internalFatalThreshold     = 3
)

type correlatedWindowState struct {
	start   time.Time
	count   int
	proxies map[ProxyKey]struct{}
}

type Supervisor struct {
	mu sync.Mutex

	owners Owners
	now    func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool

	health   map[ProxyKey]*healthState
	sequence uint64

	inflight map[ActionKey]bool

	progressSequence uint64
	lastProgress     time.Time
	lastTick         time.Time

	networkEpoch     uint64
	lastNetworkEpoch time.Time
	correlated       correlatedWindowState

	internalWindowStart time.Time
	internalFailures    int
	fatalRequested      bool
	fatalReason         string
	fatalCh             chan FatalSignal
}

func New(owners Owners) *Supervisor {
	return newWithClock(owners, time.Now)
}

func newWithClock(owners Owners, now func() time.Time) *Supervisor {
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{
		owners:   owners,
		now:      now,
		ctx:      ctx,
		cancel:   cancel,
		health:   make(map[ProxyKey]*healthState),
		inflight: make(map[ActionKey]bool),
		fatalCh:  make(chan FatalSignal, 1),
	}
}

func (s *Supervisor) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

// Record updates only Supervisor-owned transient state. It never invokes owner
// recovery actions, so callers can safely record outcomes that an owner has
// already handled without duplicating recovery work.
func (s *Supervisor) Record(outcome Outcome) {
	s.record(outcome)
}

// Recover records an outcome and schedules the smallest owner-scoped recovery
// action for failures that have not already been handled by their owner.
func (s *Supervisor) Recover(outcome Outcome) {
	triggerIdle, networkEpoch := s.record(outcome)

	switch outcome.Kind {
	case OutcomeRouteFailure:
		s.Trigger(ActionRefreshRoute)
	case OutcomeAuthExhausted:
		s.Trigger(ActionRefreshAuth)
	}
	if triggerIdle {
		s.Trigger(ActionCloseIdleTransports)
	}
	if networkEpoch {
		s.triggerNetworkRecovery()
	}
}

func (s *Supervisor) record(outcome Outcome) (triggerIdle bool, networkEpoch bool) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, false
	}

	switch {
	case outcome.Kind == OutcomeSuccess:
		if outcome.Proxy != "" {
			delete(s.health, outcome.Proxy)
		}
	case outcome.Kind.PenalizesCandidate():
		triggerIdle = s.recordLocalFailureLocked(now, outcome.Proxy)
		networkEpoch = s.observeCorrelatedFailureLocked(now, outcome.Proxy)
	case outcome.Kind == OutcomeInternalFailure:
		s.recordInternalFailureLocked(now)
	}
	return triggerIdle, networkEpoch
}

func (s *Supervisor) recordInternalFailureLocked(now time.Time) {
	if s.internalWindowStart.IsZero() || now.Sub(s.internalWindowStart) > internalFailureWindow {
		s.internalWindowStart = now
		s.internalFailures = 0
	}
	s.internalFailures++
	if s.internalFailures < internalFatalThreshold || s.fatalRequested {
		return
	}
	s.fatalRequested = true
	s.fatalReason = "repeated internal runtime failure"
	select {
	case s.fatalCh <- FatalSignal{Reason: s.fatalReason, At: now}:
	default:
	}
}

func (s *Supervisor) observeCorrelatedFailureLocked(now time.Time, key ProxyKey) bool {
	if key == "" {
		return false
	}
	if !s.lastNetworkEpoch.IsZero() && now.Sub(s.lastNetworkEpoch) < networkEpochCooldown {
		return false
	}
	if s.correlated.start.IsZero() || now.Sub(s.correlated.start) > correlatedFailureWindow {
		s.correlated.start = now
		s.correlated.count = 0
		s.correlated.proxies = make(map[ProxyKey]struct{})
	}
	if s.correlated.proxies == nil {
		s.correlated.proxies = make(map[ProxyKey]struct{})
	}
	s.correlated.count++
	s.correlated.proxies[key] = struct{}{}
	if s.correlated.count < correlatedFailureThreshold || len(s.correlated.proxies) < 2 {
		return false
	}
	s.startNetworkEpochLocked(now)
	return true
}

func (s *Supervisor) resetCorrelatedLocked() {
	s.correlated = correlatedWindowState{}
}

func (s *Supervisor) startNetworkEpochLocked(now time.Time) {
	s.networkEpoch++
	s.lastNetworkEpoch = now
	s.health = make(map[ProxyKey]*healthState)
	s.resetCorrelatedLocked()
}

// Tick records internal worker progress. Progress is local process progress and
// deliberately independent of Internet/upstream success.
func (s *Supervisor) Tick(now time.Time) {
	if now.IsZero() {
		now = s.now()
	}
	triggerEpoch := false

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.progressSequence++
	s.lastProgress = now
	if !s.lastTick.IsZero() && now.Sub(s.lastTick) > networkGapThreshold {
		s.startNetworkEpochLocked(now)
		triggerEpoch = true
	}
	s.lastTick = now
	s.mu.Unlock()

	if triggerEpoch {
		s.triggerNetworkRecovery()
	}
}
