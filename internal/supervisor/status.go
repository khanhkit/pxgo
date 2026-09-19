package supervisor

import (
	"sort"
	"time"
)

type FatalSignal struct {
	Reason string
	At     time.Time
}

type CandidateStatus struct {
	ProxyKey     ProxyKey
	Failures     int
	BackoffLevel int
	Cooling      bool
	CoolingUntil time.Time
}

// Status is a value snapshot. It intentionally contains no mutable maps or
// subsystem-owned state. Candidate health is a bounded copied diagnostic view.
type Status struct {
	ProgressSequence uint64
	LastProgress     time.Time
	NetworkEpoch     uint64
	HealthEntries    int
	InflightActions  int
	FatalRequested   bool
	FatalReason      string
	Candidates       []CandidateStatus
}

func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	candidates := make([]CandidateStatus, 0, len(s.health))
	for key, state := range s.health {
		candidates = append(candidates, CandidateStatus{
			ProxyKey:     key,
			Failures:     state.failures,
			BackoffLevel: state.backoffLevel,
			Cooling:      !state.coolingUntil.IsZero() && now.Before(state.coolingUntil),
			CoolingUntil: state.coolingUntil,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ProxyKey < candidates[j].ProxyKey
	})
	return Status{
		ProgressSequence: s.progressSequence,
		LastProgress:     s.lastProgress,
		NetworkEpoch:     s.networkEpoch,
		HealthEntries:    len(s.health),
		InflightActions:  len(s.inflight),
		FatalRequested:   s.fatalRequested,
		FatalReason:      s.fatalReason,
		Candidates:       candidates,
	}
}

func (s *Supervisor) FatalSignals() <-chan FatalSignal {
	return s.fatalCh
}
