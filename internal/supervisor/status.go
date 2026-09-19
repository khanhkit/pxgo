package supervisor

import "time"

type FatalSignal struct {
	Reason string
	At     time.Time
}

// Status is a value snapshot. It intentionally contains no mutable maps or
// subsystem-owned state.
type Status struct {
	ProgressSequence uint64
	LastProgress     time.Time
	NetworkEpoch     uint64
	HealthEntries    int
	InflightActions  int
	FatalRequested   bool
	FatalReason      string
}

func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		ProgressSequence: s.progressSequence,
		LastProgress:     s.lastProgress,
		NetworkEpoch:     s.networkEpoch,
		HealthEntries:    len(s.health),
		InflightActions:  len(s.inflight),
		FatalRequested:   s.fatalRequested,
		FatalReason:      s.fatalReason,
	}
}

func (s *Supervisor) FatalSignals() <-chan FatalSignal {
	return s.fatalCh
}
