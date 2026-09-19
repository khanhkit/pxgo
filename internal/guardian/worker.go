package guardian

import (
	"context"
	"errors"
	"net"
	"time"
)

type WorkerExit int

const (
	WorkerExitNormal         WorkerExit = 0
	WorkerExitStartupFailure WorkerExit = 5
	WorkerExitRestart        WorkerExit = 75
)

type WorkerResult struct {
	Exit WorkerExit
	Err  error
}

type WorkerHooks struct {
	Start          func() error
	Ready          func() bool
	Progress       func() uint64
	FatalRequested func() bool
	Shutdown       func(context.Context) error
	Snapshot       func()
}

type WorkerOptions struct {
	HeartbeatInterval time.Duration
	ReadyPollInterval time.Duration
	StopTimeout       time.Duration
}

func normalizeWorkerOptions(options WorkerOptions) WorkerOptions {
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = 2 * time.Second
	}
	if options.ReadyPollInterval <= 0 {
		options.ReadyPollInterval = 25 * time.Millisecond
	}
	if options.StopTimeout <= 0 {
		options.StopTimeout = 5 * time.Second
	}
	return options
}

func RunWorker(ctx context.Context, session *Session, hooks WorkerHooks, options WorkerOptions) WorkerResult {
	options = normalizeWorkerOptions(options)
	if session == nil || hooks.Start == nil || hooks.Ready == nil || hooks.Shutdown == nil {
		return WorkerResult{Exit: WorkerExitStartupFailure, Err: errors.New("guardian worker hooks incomplete")}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	startResult := make(chan error, 1)
	go func() { startResult <- hooks.Start() }()

	controlCtx, cancelControl := context.WithCancel(ctx)
	defer cancelControl()
	messages := make(chan Message, 1)
	controlErr := make(chan error, 1)
	go readWorkerControl(controlCtx, session, messages, controlErr)

	readyTicker := time.NewTicker(options.ReadyPollInterval)
	defer readyTicker.Stop()
	heartbeatTicker := time.NewTicker(options.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	readySent := false
	for {
		select {
		case err := <-startResult:
			if !readySent {
				if err == nil {
					err = errors.New("worker server exited before READY")
				}
				return WorkerResult{Exit: WorkerExitStartupFailure, Err: err}
			}
			if err != nil {
				return WorkerResult{Exit: WorkerExitRestart, Err: err}
			}
			// A clean server exit after READY is a worker-requested normal stop
			// (for example local /PxgoQuit). Tell the parent explicitly so it
			// never infers restartability from socket/process timing.
			_ = session.Send(controlCtx, Message{Type: MessageStop})
			return WorkerResult{Exit: WorkerExitNormal}

		case <-readyTicker.C:
			if readySent || !hooks.Ready() {
				continue
			}
			if err := session.Send(controlCtx, Message{Type: MessageReady}); err != nil {
				return stopWorker(hooks, startResult, options.StopTimeout, WorkerExitNormal, nil)
			}
			readySent = true

		case <-heartbeatTicker.C:
			if !readySent {
				continue
			}
			if hooks.FatalRequested != nil && hooks.FatalRequested() {
				if hooks.Snapshot != nil {
					hooks.Snapshot()
				}
				return stopWorker(hooks, startResult, options.StopTimeout, WorkerExitRestart, nil)
			}
			var sequence uint64
			if hooks.Progress != nil {
				sequence = hooks.Progress()
			}
			if err := session.Send(controlCtx, Message{Type: MessageBeat, Sequence: sequence}); err != nil {
				return stopWorker(hooks, startResult, options.StopTimeout, WorkerExitNormal, nil)
			}

		case msg := <-messages:
			if msg.Type != MessageStop {
				return stopWorker(hooks, startResult, options.StopTimeout, WorkerExitNormal, ErrProtocol)
			}
			return stopWorker(hooks, startResult, options.StopTimeout, WorkerExitNormal, nil)

		case err := <-controlErr:
			if ctx.Err() != nil {
				err = nil
			}
			return stopWorker(hooks, startResult, options.StopTimeout, WorkerExitNormal, err)

		case <-ctx.Done():
			return stopWorker(hooks, startResult, options.StopTimeout, WorkerExitNormal, nil)
		}
	}
}

func readWorkerControl(ctx context.Context, session *Session, messages chan<- Message, errc chan<- error) {
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

func stopWorker(hooks WorkerHooks, startResult <-chan error, timeout time.Duration, exit WorkerExit, resultErr error) WorkerResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	shutdownErr := hooks.Shutdown(ctx)
	cancel()
	if resultErr == nil && shutdownErr != nil {
		resultErr = shutdownErr
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-startResult:
		if resultErr == nil && err != nil {
			resultErr = err
		}
	case <-timer.C:
		if resultErr == nil {
			resultErr = context.DeadlineExceeded
		}
	}
	return WorkerResult{Exit: exit, Err: resultErr}
}
