package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/pavelsimo/pxgo/internal/diagnostic"
)

const doctorControlPath = diagnostic.DoctorControlPath

func isDoctorControlRequest(req *http.Request) bool {
	return req != nil &&
		req.Method == http.MethodGet &&
		req.URL != nil &&
		!req.URL.IsAbs() &&
		req.RequestURI == doctorControlPath
}

func (s *Server) writeDoctorResponse(rw http.ResponseWriter) {
	rw.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(rw).Encode(s.DiagnosticSnapshot()); err != nil {
		// The snapshot is best-effort diagnostics. A broken sink/encoder must not
		// influence data-plane availability or mutate service state.
		diagnostic.Record("diagnostic.error", err.Error())
	}
}

func (s *Server) BestEffortFatalSnapshot(path string) {
	diagnostic.BestEffortWriteFatalSnapshot(path, s.DiagnosticSnapshot())
}

func (s *Server) DiagnosticSnapshot() diagnostic.Snapshot {
	now := time.Now()

	s.wmu.RLock()
	route := diagnostic.RouteSnapshot{}
	if s.w != nil {
		route.Source = string(s.w.Source)
		route.Mode = s.w.Mode
		if s.w.PAC != nil {
			status := s.w.PAC.Status()
			route.PACLoaded = status.Loaded
			route.PACLastAttempt = status.LastLoadAttempt
			route.PACLastError = diagnostic.RedactText(status.LastLoadError)
		}
	}
	s.wmu.RUnlock()

	runtimeStatus := s.RuntimeStatus()
	upstreams := make([]diagnostic.UpstreamHealthSnapshot, 0, len(runtimeStatus.Candidates))
	for _, candidate := range runtimeStatus.Candidates {
		upstreams = append(upstreams, diagnostic.UpstreamHealthSnapshot{
			ProxyKey:     diagnostic.RedactText(string(candidate.ProxyKey)),
			Failures:     candidate.Failures,
			BackoffLevel: candidate.BackoffLevel,
			Cooling:      candidate.Cooling,
			CoolingUntil: candidate.CoolingUntil,
		})
	}
	snapshot := diagnostic.Snapshot{
		Ready:         s.Ready(),
		Listen:        diagnostic.RedactText(s.cfg.Listen),
		Port:          s.Port(),
		UptimeSeconds: now.Sub(s.startedAt).Seconds(),
		Route:         route,
		Auth: diagnostic.AuthSnapshot{
			UpstreamMode:      s.cfg.Auth,
			UpstreamMechanism: s.authMechanism.Snapshot(),
			ClientMode:        s.cfg.ClientAuth,
			KerberosEnabled:   s.cfg.Kerberos,
		},
		Runtime: diagnostic.RuntimeSnapshot{
			ProgressSequence: runtimeStatus.ProgressSequence,
			LastProgress:     runtimeStatus.LastProgress,
			NetworkEpoch:     runtimeStatus.NetworkEpoch,
			HealthEntries:    runtimeStatus.HealthEntries,
			InflightActions:  runtimeStatus.InflightActions,
			FatalRequested:   runtimeStatus.FatalRequested,
			FatalReason:      diagnostic.RedactText(runtimeStatus.FatalReason),
			Upstreams:        upstreams,
		},
		ActiveTunnels: s.ActiveTunnels(),
		Process:       diagnostic.ProcessMetrics(),
		ConfigSources: diagnosticConfigSources(s.cfg.Sources),
		Events:        diagnostic.Events(),
	}
	if s.krb != nil {
		status := s.krb.Status()
		snapshot.Auth.Kerberos = &diagnostic.KerberosSnapshot{
			Refreshing:   status.Refreshing,
			Closed:       status.Closed,
			TicketExpiry: status.TicketExpiry,
			NextCheck:    status.NextCheck,
			Backoff:      status.Backoff,
		}
	}
	return snapshot
}

func diagnosticConfigSources(sources map[string]string) map[string]string {
	if len(sources) == 0 {
		return nil
	}
	safe := make(map[string]string, len(sources))
	for key, source := range sources {
		safe[key] = sourceClass(source)
	}
	return safe
}

const (
	sourceClassEnv    = "env"
	sourceClassINI    = "ini"
	sourceClassDotenv = "dotenv"
)

func sourceClass(source string) string {
	source = strings.TrimSpace(source)
	switch {
	case source == "":
		return ""
	case source == "default", source == "cli":
		return source
	case strings.HasPrefix(source, sourceClassEnv+":"):
		return sourceClassEnv
	case strings.HasPrefix(source, sourceClassINI+":"):
		return sourceClassINI
	case strings.HasPrefix(source, sourceClassDotenv+":"):
		return sourceClassDotenv
	default:
		return "other"
	}
}
