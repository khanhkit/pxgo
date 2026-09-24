package main

const (
	nofileTarget = uint64(65536)
	nofileWarn   = uint64(1024)
)

var nofileFallbacks = [...]uint64{8192, 4096, 2048, 1024}

type nofileOps struct {
	get func() (soft, hard uint64, err error)
	set func(soft, hard uint64) error
}

func raiseNofileLimitWith(ops nofileOps) (uint64, error) {
	soft, hard, err := ops.get()
	if err != nil {
		return 0, err
	}
	target := nofileTarget
	if hard < target {
		target = hard
	}
	if soft >= target {
		return soft, nil
	}
	if err := ops.set(target, hard); err == nil {
		return target, nil
	}
	for _, fallback := range nofileFallbacks {
		if fallback <= soft {
			break
		}
		if fallback > hard {
			continue
		}
		if err := ops.set(fallback, hard); err == nil {
			return fallback, nil
		}
	}
	finalSoft, _, getErr := ops.get()
	if getErr != nil {
		return soft, getErr
	}
	return finalSoft, nil
}
