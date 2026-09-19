package guardian

type ExitDisposition uint8

const (
	ExitStartupFailure ExitDisposition = iota
	ExitRestart
	ExitNormalStop
)

func ClassifyExit(ready, stopping, internalRecycle bool) ExitDisposition {
	if !ready {
		return ExitStartupFailure
	}
	if stopping {
		return ExitNormalStop
	}
	if internalRecycle {
		return ExitRestart
	}
	return ExitRestart
}
