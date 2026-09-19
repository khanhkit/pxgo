package guardian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	controlAddrEnv  = "PXGOINT_GUARDIAN_ADDR"
	controlTokenEnv = "PXGOINT_GUARDIAN_TOKEN"
)

type WorkerControl struct {
	Addr  string
	Token string
}

type CommandSpec struct {
	Path   string
	Args   []string
	Env    []string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type ParentOptions struct {
	HangWindow      time.Duration
	WatchInterval   time.Duration
	StopTimeout     time.Duration
	RestartSchedule []time.Duration
	StableRunReset  time.Duration
}

type StartupExitError struct {
	Code int
	Err  error
}

func (e *StartupExitError) Error() string {
	if e == nil {
		return "guardian worker failed before readiness"
	}
	if e.Err == nil {
		return fmt.Sprintf("guardian worker failed before readiness (exit %d)", e.Code)
	}
	return fmt.Sprintf("guardian worker failed before readiness (exit %d): %v", e.Code, e.Err)
}

func (e *StartupExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type generationResult struct {
	disposition ExitDisposition
	err         error
	readyFor    time.Duration
	startupCode int
}

type acceptResult struct {
	session *Session
	err     error
}

func normalizeParentOptions(options ParentOptions) ParentOptions {
	if options.HangWindow <= 0 {
		options.HangWindow = 20 * time.Second
	}
	if options.WatchInterval <= 0 {
		options.WatchInterval = 2 * time.Second
	}
	if options.StopTimeout <= 0 {
		options.StopTimeout = 5 * time.Second
	}
	if len(options.RestartSchedule) == 0 {
		options.RestartSchedule = append([]time.Duration(nil), restartBackoffSchedule[:]...)
	} else {
		options.RestartSchedule = append([]time.Duration(nil), options.RestartSchedule...)
	}
	for i, delay := range options.RestartSchedule {
		if delay <= 0 {
			options.RestartSchedule[i] = time.Second
		}
	}
	if options.StableRunReset <= 0 {
		options.StableRunReset = stableRunReset
	}
	return options
}

func WorkerControlFromEnv() (WorkerControl, bool, error) {
	addr := strings.TrimSpace(os.Getenv(controlAddrEnv))
	token := strings.TrimSpace(os.Getenv(controlTokenEnv))
	if addr == "" && token == "" {
		return WorkerControl{}, false, nil
	}
	if addr == "" || token == "" {
		return WorkerControl{}, false, errors.New("guardian control environment incomplete")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return WorkerControl{}, false, errors.New("guardian control address invalid")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return WorkerControl{}, false, errors.New("guardian control address must be loopback")
	}
	if len(token) != 64 {
		return WorkerControl{}, false, errors.New("guardian control token invalid")
	}
	for _, r := range token {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return WorkerControl{}, false, errors.New("guardian control token invalid")
		}
	}
	return WorkerControl{Addr: addr, Token: token}, true, nil
}

func RunParent(ctx context.Context, spec CommandSpec, options ParentOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(spec.Path) == "" {
		return errors.New("guardian worker command path is empty")
	}
	options = normalizeParentOptions(options)
	restartLevel := 0

	for {
		result := runGeneration(ctx, spec, options)
		switch result.disposition {
		case ExitNormalStop:
			return result.err
		case ExitStartupFailure:
			return &StartupExitError{Code: result.startupCode, Err: result.err}
		case ExitRestart:
			if result.readyFor >= options.StableRunReset {
				restartLevel = 0
			}
			delay := parentRestartDelay(options.RestartSchedule, restartLevel)
			if restartLevel < len(options.RestartSchedule)-1 {
				restartLevel++
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return nil
			}
		default:
			return errors.New("guardian worker returned unknown disposition")
		}
	}
}

func runGeneration(ctx context.Context, spec CommandSpec, options ParentOptions) generationResult {
	listener, err := Listen()
	if err != nil {
		return generationResult{disposition: ExitStartupFailure, err: err}
	}
	defer listener.Close()

	control := WorkerControl{Addr: listener.Addr(), Token: listener.Token()}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = withWorkerControlEnv(spec.Env, control)
	cmd.Dir = spec.Dir
	cmd.Stdin = spec.Stdin
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	if err := cmd.Start(); err != nil {
		return generationResult{disposition: ExitStartupFailure, err: err}
	}

	waitc := make(chan error, 1)
	go func() { waitc <- cmd.Wait() }()

	acceptCtx, cancelAccept := context.WithCancel(ctx)
	defer cancelAccept()
	acceptc := make(chan acceptResult, 1)
	go func() {
		session, acceptErr := listener.Accept(acceptCtx)
		acceptc <- acceptResult{session: session, err: acceptErr}
	}()

	select {
	case <-ctx.Done():
		cancelAccept()
		_ = listener.Close()
		stopErr := stopChild(nil, cmd, waitc, options.StopTimeout)
		return generationResult{disposition: ExitNormalStop, err: normalizeParentStopError(stopErr)}
	case waitErr := <-waitc:
		cancelAccept()
		_ = listener.Close()
		return generationResult{
			disposition: ExitStartupFailure,
			err:         waitErr,
			startupCode: exitCode(waitErr),
		}
	case accepted := <-acceptc:
		if accepted.err != nil {
			if ctx.Err() != nil {
				stopErr := stopChild(nil, cmd, waitc, options.StopTimeout)
				return generationResult{disposition: ExitNormalStop, err: normalizeParentStopError(stopErr)}
			}
			stopErr := stopChild(nil, cmd, waitc, options.StopTimeout)
			return generationResult{
				disposition: ExitStartupFailure,
				err:         errors.Join(accepted.err, stopErr),
				startupCode: exitCode(stopErr),
			}
		}
		return monitorReadyWorker(ctx, accepted.session, cmd, waitc, options)
	}
}

func monitorReadyWorker(
	ctx context.Context,
	session *Session,
	cmd *exec.Cmd,
	waitc <-chan error,
	options ParentOptions,
) generationResult {
	defer session.Close()

	messages := make(chan Message, 1)
	readErr := make(chan error, 1)
	readCtx, cancelRead := context.WithCancel(context.Background())
	defer cancelRead()
	go readParentControl(readCtx, session, messages, readErr)

	watchdog := NewWatchdog(options.HangWindow)
	ticker := time.NewTicker(options.WatchInterval)
	defer ticker.Stop()

	ready := false
	var readyAt time.Time
	for {
		select {
		case <-ctx.Done():
			stopErr := stopChild(session, cmd, waitc, options.StopTimeout)
			return generationResult{
				disposition: ExitNormalStop,
				err:         normalizeParentStopError(stopErr),
				readyFor:    readyDuration(readyAt),
			}

		case waitErr := <-waitc:
			if !ready {
				return generationResult{
					disposition: ExitStartupFailure,
					err:         waitErr,
					startupCode: exitCode(waitErr),
				}
			}
			return generationResult{
				disposition: ExitRestart,
				err:         waitErr,
				readyFor:    readyDuration(readyAt),
			}

		case msg := <-messages:
			now := time.Now()
			switch msg.Type {
			case MessageReady:
				if ready {
					stopErr := stopChild(session, cmd, waitc, options.StopTimeout)
					return generationResult{
						disposition: ExitRestart,
						err:         errors.Join(ErrProtocol, stopErr),
						readyFor:    readyDuration(readyAt),
					}
				}
				ready = true
				readyAt = now
				watchdog.Ready(now)
			case MessageBeat:
				if !ready {
					stopErr := stopChild(session, cmd, waitc, options.StopTimeout)
					return generationResult{
						disposition: ExitStartupFailure,
						err:         errors.Join(ErrProtocol, stopErr),
						startupCode: exitCode(stopErr),
					}
				}
				watchdog.Beat(msg.Sequence, now)
			default:
				stopErr := stopChild(session, cmd, waitc, options.StopTimeout)
				disposition := ExitStartupFailure
				if ready {
					disposition = ExitRestart
				}
				return generationResult{
					disposition: disposition,
					err:         errors.Join(ErrProtocol, stopErr),
					readyFor:    readyDuration(readyAt),
					startupCode: exitCode(stopErr),
				}
			}

		case err := <-readErr:
			stopErr := stopChild(session, cmd, waitc, options.StopTimeout)
			if !ready {
				return generationResult{
					disposition: ExitStartupFailure,
					err:         errors.Join(err, stopErr),
					startupCode: exitCode(stopErr),
				}
			}
			return generationResult{
				disposition: ExitRestart,
				err:         errors.Join(err, stopErr),
				readyFor:    readyDuration(readyAt),
			}

		case now := <-ticker.C:
			if !ready || !watchdog.Check(now) {
				continue
			}
			stopErr := stopChild(session, cmd, waitc, options.StopTimeout)
			return generationResult{
				disposition: ExitRestart,
				err:         stopErr,
				readyFor:    readyDuration(readyAt),
			}
		}
	}
}

func readParentControl(ctx context.Context, session *Session, messages chan<- Message, errc chan<- error) {
	for {
		msg, err := session.Read(ctx)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() && ctx.Err() == nil {
				continue
			}
			select {
			case errc <- err:
			case <-ctx.Done():
			}
			return
		}
		select {
		case messages <- msg:
		case <-ctx.Done():
			return
		}
	}
}

func stopChild(session *Session, cmd *exec.Cmd, waitc <-chan error, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), timeout)
	if session != nil {
		_ = session.Send(stopCtx, Message{Type: MessageStop})
	}
	cancel()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waitc:
		return err
	case <-timer.C:
	}

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	reapTimer := time.NewTimer(timeout)
	defer reapTimer.Stop()
	select {
	case err := <-waitc:
		return err
	case <-reapTimer.C:
		return context.DeadlineExceeded
	}
}

func withWorkerControlEnv(base []string, control WorkerControl) []string {
	if base == nil {
		base = os.Environ()
	}
	result := make([]string, 0, len(base)+2)
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == controlAddrEnv || key == controlTokenEnv) {
			continue
		}
		result = append(result, entry)
	}
	result = append(result, controlAddrEnv+"="+control.Addr)
	result = append(result, controlTokenEnv+"="+control.Token)
	return result
}

func parentRestartDelay(schedule []time.Duration, level int) time.Duration {
	if len(schedule) == 0 {
		return time.Second
	}
	if level < 0 {
		level = 0
	}
	if level >= len(schedule) {
		level = len(schedule) - 1
	}
	delay := schedule[level]
	if delay <= 0 {
		return time.Second
	}
	return delay
}

func readyDuration(readyAt time.Time) time.Duration {
	if readyAt.IsZero() {
		return 0
	}
	return time.Since(readyAt)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func normalizeParentStopError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A child force-killed during an explicit parent stop is still a normal
		// parent lifecycle outcome; the process has been reaped successfully.
		return nil
	}
	return err
}
