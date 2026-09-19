package diagnostic

import "time"

const DoctorControlPath = "/PxgoDoctor"

type ProcessSnapshot struct {
	CPUSeconds float64 `json:"cpu_seconds"`
	RSSBytes   uint64  `json:"rss_bytes"`
	Goroutines int     `json:"goroutines"`
	Error      string  `json:"error,omitempty"`
}

type RouteSnapshot struct {
	Source         string    `json:"source"`
	Mode           int       `json:"mode"`
	PACLoaded      bool      `json:"pac_loaded"`
	PACLastAttempt time.Time `json:"pac_last_attempt,omitempty"`
	PACLastError   string    `json:"pac_last_error,omitempty"`
}

type AuthSnapshot struct {
	UpstreamMode      string            `json:"upstream_mode"`
	UpstreamMechanism string            `json:"upstream_mechanism,omitempty"`
	ClientMode        string            `json:"client_mode"`
	KerberosEnabled   bool              `json:"kerberos_enabled"`
	Kerberos          *KerberosSnapshot `json:"kerberos,omitempty"`
}

type KerberosSnapshot struct {
	Refreshing   bool          `json:"refreshing"`
	Closed       bool          `json:"closed"`
	TicketExpiry time.Time     `json:"ticket_expiry,omitempty"`
	NextCheck    time.Time     `json:"next_check,omitempty"`
	Backoff      time.Duration `json:"backoff"`
}

type UpstreamHealthSnapshot struct {
	ProxyKey     string    `json:"proxy_key"`
	Failures     int       `json:"failures"`
	BackoffLevel int       `json:"backoff_level"`
	Cooling      bool      `json:"cooling"`
	CoolingUntil time.Time `json:"cooling_until,omitempty"`
}

type RuntimeSnapshot struct {
	ProgressSequence uint64                   `json:"progress_sequence"`
	LastProgress     time.Time                `json:"last_progress,omitempty"`
	NetworkEpoch     uint64                   `json:"network_epoch"`
	HealthEntries    int                      `json:"health_entries"`
	InflightActions  int                      `json:"inflight_actions"`
	FatalRequested   bool                     `json:"fatal_requested"`
	FatalReason      string                   `json:"fatal_reason,omitempty"`
	Upstreams        []UpstreamHealthSnapshot `json:"upstreams,omitempty"`
}

type DoctorReport struct {
	Live     bool     `json:"live"`
	Error    string   `json:"error,omitempty"`
	Snapshot Snapshot `json:"snapshot"`
}

type Snapshot struct {
	Ready         bool              `json:"ready"`
	Listen        string            `json:"listen"`
	Port          int               `json:"port"`
	UptimeSeconds float64           `json:"uptime_seconds"`
	Route         RouteSnapshot     `json:"route"`
	Auth          AuthSnapshot      `json:"auth"`
	Runtime       RuntimeSnapshot   `json:"runtime"`
	ActiveTunnels int64             `json:"active_tunnels"`
	Process       ProcessSnapshot   `json:"process"`
	ConfigSources map[string]string `json:"config_sources,omitempty"`
	Events        []Event           `json:"events"`
}
