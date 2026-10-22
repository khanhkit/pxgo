package proxy

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Experimental, mutually-exclusive classification *accounting* within ONE
// global physical admission gate. Soft classes are not independent physical
// semaphore permits and can therefore never multiply the hard FD/memory cap.
type connectionClass uint8

const (
	classProvisional connectionClass = iota
	classShort
	classLong
)

const (
	classDurationThreshold = 30 * time.Second
	classRequestThreshold  = 8
)

type connectionClassContextKey struct{}

type connectionClassManager struct {
	mu                       sync.Mutex
	live                     map[*connectionClassLease]struct{}
	provisional, short, long int
	shortBudget, longBudget  int
	promoted                 uint64
	longOverflow             uint64
	closed                   bool
}

type connectionClassLease struct {
	manager  *connectionClassManager
	opened   time.Time
	kind     connectionClass
	requests int
	closed   bool // manager.mu owns all lease state
}

type connectionClassSnapshot struct {
	Provisional, Short, Long int
	ShortBudget, LongBudget  int
	Promoted, LongOverflow   uint64
	Total                    int
}

func newConnectionClassManager() *connectionClassManager {
	return &connectionClassManager{live: make(map[*connectionClassLease]struct{})}
}

func (m *connectionClassManager) open(now time.Time) *connectionClassLease {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	l := &connectionClassLease{manager: m, opened: now, kind: classProvisional}
	m.live[l] = struct{}{}
	m.provisional++
	return l
}

func (l *connectionClassLease) finish() {
	if l == nil {
		return
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	delete(m.live, l)
	switch l.kind {
	case classProvisional:
		m.provisional--
	case classShort:
		m.short--
	case classLong:
		m.long--
	}
}

func (m *connectionClassManager) transitionLocked(l *connectionClassLease, kind connectionClass) {
	if l.closed || l.kind == kind {
		return
	}
	// Once long, never demote while the connection stays alive. This avoids
	// quota oscillation, permit churn and split accounting during keep-alive.
	if l.kind == classLong {
		return
	}
	old := l.kind
	switch old {
	case classProvisional:
		m.provisional--
	case classShort:
		m.short--
	}
	if kind == classLong {
		m.long++
		if old == classShort {
			m.promoted++
		}
		if m.longBudget > 0 && m.long > m.longBudget {
			m.longOverflow++
		}
	} else {
		m.short++
	}
	l.kind = kind
}

func (l *connectionClassLease) classifyRequest(req *http.Request, now time.Time) {
	if l == nil || req == nil {
		return
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.closed {
		return
	}
	l.requests++
	// Request-level prediction after client authorization, not based on source
	// IP, user identity, peer secrets or a forged header alone to admit sockets.
	long := req.Method == http.MethodConnect || strings.EqualFold(req.Header.Get(headerUpgrade), "websocket") || strings.Contains(strings.ToLower(req.Header.Get("Accept")), "text/event-stream")
	if !long && (now.Sub(l.opened) >= classDurationThreshold || l.requests >= classRequestThreshold) {
		long = true
	}
	if long {
		m.transitionLocked(l, classLong)
	} else {
		m.transitionLocked(l, classShort)
	}
}

func (m *connectionClassManager) promoteAged(now time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for l := range m.live {
		if l.kind == classShort && now.Sub(l.opened) >= classDurationThreshold {
			m.transitionLocked(l, classLong)
		}
	}
}

func (m *connectionClassManager) sampleBudgets(globalSoft int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if globalSoft < 0 {
		globalSoft = 0
	}
	// Reservation intent: 1/4 of the global soft budget for short requests,
	// 3/4 for long tunnels. These are SOFT accounting/debt targets. Physical
	// admission remains global and independent of these late predictions.
	m.shortBudget = globalSoft / 4
	m.longBudget = globalSoft - m.shortBudget
}

func (m *connectionClassManager) snapshot() connectionClassSnapshot {
	if m == nil {
		return connectionClassSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return connectionClassSnapshot{Provisional: m.provisional, Short: m.short, Long: m.long, ShortBudget: m.shortBudget, LongBudget: m.longBudget, Promoted: m.promoted, LongOverflow: m.longOverflow, Total: len(m.live)}
}

func (m *connectionClassManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	// No forced closure: existing admitted sockets retain their own tokens.
}

func connectionClassFromContext(ctx context.Context) *connectionClassLease {
	l, _ := ctx.Value(connectionClassContextKey{}).(*connectionClassLease)
	return l
}

// This flag has NO meaning in legacy/shadow and does not enable AUTO itself.
func experimentalConnectionClassesEnabled() bool {
	return os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") == "1"
}
