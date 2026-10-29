package proxy

import (
	"context"
	"mime"
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
	longDenied               uint64 // rejected new authenticated long requests only
	strictLongIntake         bool   // experimental, never changes physical permits
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
	LongDenied               uint64
	LongDebt                 int
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

// classifyRequest returns false only for a new, explicitly long request
// denied by the optional post-authentication long-intake guard. Existing
// long tunnels and automatic short->long promotion are never evicted.
func (l *connectionClassLease) classifyRequest(req *http.Request, now time.Time) bool {
	if l == nil || req == nil {
		return true // preserve historical behavior for internal/no-context calls
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.closed {
		return true
	}
	l.requests++
	// Classification happens after downstream client authorization.
	explicitLong := req.Method == http.MethodConnect || strings.EqualFold(req.Header.Get(headerUpgrade), "websocket") || acceptsEventStream(req.Header)
	agedLong := now.Sub(l.opened) >= classDurationThreshold || l.requests >= classRequestThreshold
	if explicitLong || agedLong {
		// Guard only NEW explicit long requests, never an already admitted
		// connection that aged into long or a tunnel already in progress.
		// A budget shrink may leave debt; refuse new longs until it drains.
		if m.strictLongIntake && explicitLong && l.kind != classLong && m.long >= m.longBudget {
			m.longDenied++
			return false
		}
		m.transitionLocked(l, classLong)
	} else {
		m.transitionLocked(l, classShort)
	}
	return true
}

// acceptsEventStream honors the client's HTTP Accept quality weights.
// A literal text/event-stream;q=0 expressly rejects SSE and MUST NOT
// create or deny a long admission. The actual upstream response may
// still return SSE, which is classified after RoundTrip instead.
func acceptsEventStream(header http.Header) bool {
	for _, value := range header.Values("Accept") {
		for _, part := range strings.Split(value, ",") {
			mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
				continue
			}
			if quality, hasQuality := params["q"]; hasQuality && !positiveHTTPQuality(quality) {
				continue
			}
			return true
		}
	}
	return false
}

// positiveHTTPQuality accepts RFC qvalue syntax (at most three decimal
// digits) and rejects q=0, exponent notation, hex floats and invalid >1.
func positiveHTTPQuality(q string) bool {
	if len(q) == 0 || len(q) > 5 || (q[0] != '0' && q[0] != '1') {
		return false
	}
	if len(q) == 1 {
		return q == "1"
	}
	if q[1] != '.' {
		return false
	}
	positive := q[0] == '1'
	for i := 2; i < len(q); i++ {
		if q[i] < '0' || q[i] > '9' || (q[0] == '1' && q[i] != '0') {
			return false
		}
		positive = positive || q[i] != '0'
	}
	return positive
}

// classifySSEResponse accounts for a genuine upstream SSE body even if the
// client sent Accept: application/json. It cannot acquire another physical
// permit: the same authenticated downstream lease transfers short->long.
// Under the opt-in strict guard, refuse a new unexpected long response if
// the long class budget is full; existing long leases are never evicted.
func (l *connectionClassLease) classifySSEResponse(resp *http.Response) bool {
	if l == nil || resp == nil || resp.StatusCode != http.StatusOK ||
		!isSSEMediaType(resp.Header.Get("Content-Type")) {
		return true
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.closed {
		return true
	}
	if m.strictLongIntake && l.kind != classLong && m.long >= m.longBudget {
		m.longDenied++
		return false
	}
	m.transitionLocked(l, classLong)
	return true
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
	// Reservation intent: 1/4 of the actual effective physical admission
	// window for short requests, 3/4 for long tunnels. These are class budgets;
	// a separate opt-in guard may reject new authenticated long upgrades. Physical
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
	return connectionClassSnapshot{Provisional: m.provisional, Short: m.short, Long: m.long, ShortBudget: m.shortBudget, LongBudget: m.longBudget, Promoted: m.promoted, LongOverflow: m.longOverflow, LongDenied: m.longDenied, LongDebt: max(0, m.long-m.longBudget), Total: len(m.live)}
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

// Long-intake guard is research-only. It requires AUTO and class tracking,
// rejects only new authenticated CONNECT/WebSocket/SSE admissions when at the
// long budget, and cannot guarantee short liveness against preauth slowloris
// or long debt after keep-alive promotion.
func experimentalStrictLongIntakeEnabled() bool {
	return os.Getenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE") == "1"
}
