package proxy

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestR69CanceledAdmissionMustNotTakeAvailablePhysicalPermit(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	canceled := make(chan struct{})
	close(canceled)
	err := g.acquire(canceled, 1*time.Second)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("R69_CANCELLED_LISTENER_ADMITTED: err=%v (want net.ErrClosed)", err)
	}
	g.mu.Lock()
	active, waiting, queued := g.active, g.waiting, len(g.waitQueue)
	g.mu.Unlock()
	if active != 0 || waiting != 0 || queued != 0 {
		t.Fatalf("canceled listener consumed permit: active=%d waiting=%d queued=%d", active, waiting, queued)
	}
}
