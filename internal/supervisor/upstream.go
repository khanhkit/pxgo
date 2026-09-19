package supervisor

import "time"

const (
	localFailureThreshold = 2
	maxHealthEntries      = 256
	maxBackoffLevel       = 5
	baseCooldown          = 2 * time.Second
	maxCooldown           = 30 * time.Second
)

type healthState struct {
	failures     int
	backoffLevel int
	coolingUntil time.Time
	lastSequence uint64
}

func cooldownForLevel(level int) time.Duration {
	if level <= 0 {
		return 0
	}
	d := baseCooldown << (level - 1)
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}

func (s *Supervisor) recordLocalFailureLocked(now time.Time, key ProxyKey) (enteredCooling bool) {
	if key == "" {
		return false
	}
	state := s.health[key]
	if state == nil {
		if len(s.health) >= maxHealthEntries {
			s.evictOldestHealthLocked()
		}
		state = &healthState{}
		s.health[key] = state
	}
	s.sequence++
	state.lastSequence = s.sequence

	if !state.coolingUntil.IsZero() && !now.Before(state.coolingUntil) {
		state.failures = 0
		state.backoffLevel = 0
		state.coolingUntil = time.Time{}
	}

	state.failures++
	if state.failures < localFailureThreshold {
		return false
	}
	enteredCooling = state.coolingUntil.IsZero()
	if state.backoffLevel < maxBackoffLevel {
		state.backoffLevel++
	}
	state.coolingUntil = now.Add(cooldownForLevel(state.backoffLevel))
	return enteredCooling
}

func (s *Supervisor) evictOldestHealthLocked() {
	var (
		oldestKey ProxyKey
		oldestSeq uint64
		have      bool
	)
	for key, state := range s.health {
		if !have || state.lastSequence < oldestSeq {
			oldestKey = key
			oldestSeq = state.lastSequence
			have = true
		}
	}
	if have {
		delete(s.health, oldestKey)
	}
}

func (s *Supervisor) candidateCooledLocked(now time.Time, key ProxyKey) bool {
	state := s.health[key]
	if state == nil || state.coolingUntil.IsZero() {
		return false
	}
	if now.Before(state.coolingUntil) {
		return true
	}
	delete(s.health, key)
	return false
}

// Order returns a reordered copy of the authoritative candidate list. DIRECT
// slots and membership are preserved exactly; only non-DIRECT members may swap
// among the original non-DIRECT slots.
func (s *Supervisor) Order(authoritative []Candidate) []Candidate {
	out := append([]Candidate(nil), authoritative...)
	if len(out) < 2 {
		return out
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	explicitCount := 0
	cooledCount := 0
	healthy := make([]Candidate, 0, len(out))
	cooled := make([]Candidate, 0, len(out))
	for _, c := range out {
		if c.Direct {
			continue
		}
		explicitCount++
		if s.candidateCooledLocked(now, c.Key) {
			cooledCount++
			cooled = append(cooled, c)
		} else {
			healthy = append(healthy, c)
		}
	}
	if explicitCount == 0 || cooledCount == 0 || cooledCount == explicitCount {
		return out
	}

	reordered := append([]Candidate(nil), healthy...)
	reordered = append(reordered, cooled...)
	index := 0
	for i := range out {
		if out[i].Direct {
			continue
		}
		out[i] = reordered[index]
		index++
	}
	return out
}

// ResetTransient discards only Supervisor-owned transient network health.
func (s *Supervisor) ResetTransient() {
	s.mu.Lock()
	s.health = make(map[ProxyKey]*healthState)
	s.resetCorrelatedLocked()
	s.mu.Unlock()
}
