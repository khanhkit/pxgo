package kerberos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	CheckInterval = 300 * time.Second
	RetryInterval = 60 * time.Second
	RenewalMargin = 10 * time.Minute
	kinitCommand  = "kinit"
	klistCommand  = "klist"
)

type commandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

var (
	commandRunner       = defaultCommandRunner
	kinitPasswordRunner = defaultKinitPasswordRunner
)

type Manager struct {
	Principal    string
	PasswordFunc func() *string
	CCacheName   string
	Env          map[string]string
	IsHeimdal    bool

	TicketExpiry time.Time
	NextCheck    time.Time
	Backoff      time.Duration

	mu         sync.Mutex
	refreshing bool

	KinitWithPasswordFunc func() bool
	KinitRenewFunc        func() bool
	KlistValidFunc        func() bool
}

func New(principal string, passwordFunc func() *string, isHeimdal bool) *Manager {
	ccache := "FILE:" + filepath.Join(os.TempDir(), "krb5cc_px_"+itoa(os.Getpid()))
	env := map[string]string{}
	for _, item := range os.Environ() {
		k, v, ok := strings.Cut(item, "=")
		if ok {
			env[k] = v
		}
	}
	env["KRB5CCNAME"] = ccache
	m := &Manager{
		Principal:    principal,
		PasswordFunc: passwordFunc,
		CCacheName:   ccache,
		Env:          env,
		IsHeimdal:    isHeimdal,
	}
	m.KinitWithPasswordFunc = m.KinitWithPassword
	m.KinitRenewFunc = m.KinitRenew
	m.KlistValidFunc = m.KlistValid
	return m
}

func defaultCommandRunner(timeout time.Duration, args []string, env map[string]string, stdin string) (commandResult, error) {
	if len(args) == 0 {
		return commandResult{}, errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) // #nosec G204 -- callers pass fixed Kerberos command names with controlled arguments.
	if env != nil {
		cmd.Env = envSlice(env)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	result := commandResult{Stdout: string(out)}
	if ee := new(exec.ExitError); errors.As(err, &ee) {
		result.Stderr = string(ee.Stderr)
		result.ExitCode = ee.ExitCode()
		return result, nil
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return result, context.DeadlineExceeded
		}
		return result, err
	}
	return result, nil
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	n := i
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}

func (m *Manager) Check(force bool) *bool {
	now := time.Now()
	m.mu.Lock()
	if !force && !m.NextCheck.IsZero() && now.Before(m.NextCheck) {
		m.mu.Unlock()
		return nil
	}
	if m.refreshing {
		m.mu.Unlock()
		return nil
	}
	m.refreshing = true
	m.mu.Unlock()

	go m.refresh(force)
	return nil
}

func (m *Manager) refresh(force bool) {
	defer func() {
		m.mu.Lock()
		m.refreshing = false
		m.mu.Unlock()
	}()

	now := time.Now()
	expiry := m.expirySnapshot()

	if !expiry.IsZero() && now.Before(expiry.Add(-RenewalMargin)) {
		if m.KlistValidFunc() {
			m.setNextCheck(nextCheckFor(now, expiry))
			return
		}
	}

	// A still-valid ticket inside the renewal margin must get a renewal attempt
	// even if a previous acquisition failure left a backoff pending. Delaying it
	// until the backoff expires can let the current generation expire needlessly.
	if !expiry.IsZero() && now.Before(expiry) {
		if m.KinitRenewFunc() {
			return
		}
	}

	if !force && m.consumeBackoff(now) {
		return
	}

	_ = m.KinitWithPasswordFunc()
}

func (m *Manager) expirySnapshot() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.TicketExpiry
}

func (m *Manager) setNextCheck(next time.Time) {
	m.mu.Lock()
	m.NextCheck = next
	m.mu.Unlock()
}

func (m *Manager) setBackoff(backoff time.Duration) {
	m.mu.Lock()
	m.Backoff = backoff
	m.mu.Unlock()
}

func (m *Manager) consumeBackoff(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Backoff <= 0 {
		return false
	}
	m.NextCheck = now.Add(m.Backoff)
	m.Backoff = 0
	return true
}

func nextCheckFor(now, expiry time.Time) time.Time {
	next := now.Add(CheckInterval)
	renewAt := expiry.Add(-RenewalMargin)
	if next.After(renewAt) {
		next = renewAt
	}
	return next
}

func (m *Manager) ParseAndSetExpiry(klistOutput string) {
	expiry, ok := ParseExpiry(klistOutput, m.IsHeimdal)
	m.commitExpiry(expiry, ok, time.Now())
}

func (m *Manager) commitExpiry(expiry time.Time, ok bool, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ok {
		m.TicketExpiry = time.Time{}
		m.NextCheck = now.Add(RetryInterval)
		return
	}
	m.TicketExpiry = expiry
	m.NextCheck = nextCheckFor(now, expiry)
}

func DetectHeimdal() bool {
	result, err := commandRunner(5*time.Second, []string{klistCommand, "--version"}, nil, "")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(result.Stdout+result.Stderr), "heimdal")
}

func (m *Manager) KinitWithPassword() bool {
	password := m.PasswordFunc()
	if password == nil {
		return false
	}
	result, err := kinitPasswordRunner(30*time.Second, m.Principal, m.Env, *password)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			m.setBackoff(RetryInterval)
		case errors.Is(err, exec.ErrNotFound) || os.IsNotExist(err):
			m.setBackoff(CheckInterval)
		default:
			m.setBackoff(RetryInterval)
		}
		return false
	}
	if result.ExitCode != 0 {
		errText := strings.ToLower(result.Stderr)
		switch {
		case strings.Contains(errText, "expired"),
			strings.Contains(errText, "revoked"),
			strings.Contains(errText, "preauthentication failed"),
			strings.Contains(errText, "password incorrect"),
			strings.Contains(errText, "not found"),
			strings.Contains(errText, "unknown"),
			strings.Contains(errText, "skew"):
			m.setBackoff(CheckInterval)
		default:
			m.setBackoff(RetryInterval)
		}
		return false
	}
	m.setBackoff(0)
	m.UpdateExpiry()
	return true
}

func (m *Manager) KinitRenew() bool {
	result, err := commandRunner(5*time.Second, []string{kinitCommand, "-R"}, m.Env, "")
	if err != nil || result.ExitCode != 0 {
		return false
	}
	m.UpdateExpiry()
	return true
}

func (m *Manager) KlistValid() bool {
	flag := "-s"
	if m.IsHeimdal {
		flag = "--test"
	}
	result, err := commandRunner(5*time.Second, []string{klistCommand, flag}, m.Env, "")
	if err != nil {
		return false
	}
	stderr := strings.ToLower(result.Stderr)
	if strings.Contains(stderr, "unrecognized") || strings.Contains(stderr, "unknown") || strings.Contains(stderr, "illegal") {
		return m.KlistParseValid()
	}
	return result.ExitCode == 0
}

func (m *Manager) RunKlist() (string, bool) {
	result, err := commandRunner(5*time.Second, []string{klistCommand}, m.Env, "")
	if err != nil || result.ExitCode != 0 {
		return "", false
	}
	return result.Stdout, true
}

func (m *Manager) KlistParseValid() bool {
	output, ok := m.RunKlist()
	return ok && strings.Contains(strings.ToLower(output), "krbtgt")
}

func (m *Manager) UpdateExpiry() {
	output, ok := m.RunKlist()
	if !ok {
		m.commitExpiry(time.Time{}, false, time.Now())
		return
	}
	m.ParseAndSetExpiry(output)
}

func ParseExpiry(output string, heimdal bool) (time.Time, bool) {
	if heimdal {
		return parseHeimdal(output)
	}
	return parseMIT(output)
}

var (
	mitExpiryRe     = regexp.MustCompile(`(?m)^\s*\d{1,2}/\d{1,2}/\d{2,4}\s+\d{2}:\d{2}:\d{2}\s+(\d{1,2}/\d{1,2}/\d{2,4}\s+\d{2}:\d{2}:\d{2})\s+krbtgt/`)
	heimdalExpiryRe = regexp.MustCompile(`(?m)^\s*[A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4}\s+([A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4})\s+krbtgt/`)
)

func parseMIT(output string) (time.Time, bool) {
	matches := mitExpiryRe.FindStringSubmatch(output)
	if len(matches) != 2 {
		return time.Time{}, false
	}
	for _, layout := range []string{"1/2/2006 15:04:05", "1/2/06 15:04:05"} {
		if t, err := time.ParseInLocation(layout, matches[1], time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseHeimdal(output string) (time.Time, bool) {
	matches := heimdalExpiryRe.FindStringSubmatch(output)
	if len(matches) != 2 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("Jan 2 15:04:05 2006", matches[1], time.Local)
	return t, err == nil
}

func (m *Manager) Cleanup() {
	path := strings.TrimPrefix(m.CCacheName, "FILE:")
	if path != m.CCacheName {
		_ = os.Remove(path)
	}
}
