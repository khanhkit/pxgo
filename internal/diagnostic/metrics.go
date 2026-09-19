package diagnostic

import "runtime"

func ProcessMetrics() ProcessSnapshot {
	cpu, rss, err := platformProcessMetrics()
	result := ProcessSnapshot{
		CPUSeconds: cpu,
		RSSBytes:   rss,
		Goroutines: runtime.NumGoroutine(),
	}
	if err != nil {
		result.Error = RedactText(err.Error())
	}
	return result
}
