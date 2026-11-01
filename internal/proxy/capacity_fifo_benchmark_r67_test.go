package proxy

import (
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func BenchmarkR67GlobalPhysicalAcquireRelease(b *testing.B) {
	g := newCapacityGovernor(modeAuto, config.Default())
	done := make(chan struct{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := g.acquire(done, 2*time.Second); err != nil {
			b.Fatal(err)
		}
		g.release()
	}
	b.StopTimer()
	g.close()
}
