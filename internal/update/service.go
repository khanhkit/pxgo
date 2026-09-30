package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
)

type Status struct {
	Current   string
	Latest    string
	Available bool
	Provider  Provider
	Channel   Channel
	Release   Release
	Applied   bool
	Deferred  bool
}

type PreparedUpdate struct {
	Status Status
	staged *StagedCandidate
}

type Service struct {
	Checker    Checker
	Stager     Stager
	Runner     CommandRunner
	Provider   Provider
	Channel    Channel
	Executable string
	GOOS       string
	GOARCH     string
}

func (s Service) Check(ctx context.Context, current string) (Status, error) {
	if ctx == nil {
		return Status{}, errors.New("nil context")
	}
	executable := s.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return Status{}, fmt.Errorf("resolve executable for update provider: %w", err)
		}
	}
	goos := s.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	configured := s.Provider
	if configured == "" {
		configured = ProviderAuto
	}
	provider, err := ResolveProvider(configured, executable, goos)
	if err != nil {
		return Status{}, err
	}
	channel := s.Channel
	if channel == "" {
		channel = Stable
	}
	if _, err := ParseChannel(string(channel)); err != nil {
		return Status{}, err
	}
	checker := s.Checker
	checker.Channel = channel
	result, err := checker.Check(ctx, current)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Current:   result.Current,
		Latest:    result.Latest,
		Available: result.Available,
		Provider:  provider,
		Channel:   channel,
		Release:   result.Release,
	}, nil
}

func (p *PreparedUpdate) Cleanup() error {
	if p == nil || p.staged == nil {
		return nil
	}
	err := p.staged.Cleanup()
	p.staged = nil
	return err
}

func (s Service) Prepare(ctx context.Context, current string) (PreparedUpdate, error) {
	status, err := s.Check(ctx, current)
	if err != nil {
		return PreparedUpdate{}, err
	}
	prepared := PreparedUpdate{Status: status}
	if !status.Available || status.Provider != ProviderDirect {
		return prepared, nil
	}

	stager := s.Stager
	checker := s.Checker
	checker.Channel = status.Channel
	stager.Checker = checker
	if stager.GOOS == "" {
		stager.GOOS = s.GOOS
	}
	if stager.GOARCH == "" {
		stager.GOARCH = s.GOARCH
	}
	check := CheckResult{
		Current:   status.Current,
		Latest:    status.Latest,
		Available: status.Available,
		Release:   status.Release,
	}
	staged, err := stager.StageCheck(ctx, check)
	if err != nil {
		return prepared, err
	}
	prepared.staged = &staged
	return prepared, nil
}

func (s Service) ApplyPrepared(ctx context.Context, prepared *PreparedUpdate) (Status, error) {
	return s.ApplyPreparedWithRestart(ctx, prepared, nil)
}

func (s Service) ApplyPreparedWithRestart(ctx context.Context, prepared *PreparedUpdate, restartArgs []string) (Status, error) {
	if prepared == nil {
		return Status{}, errors.New("nil prepared update")
	}
	status := prepared.Status
	if !status.Available {
		return status, nil
	}
	if status.Provider != ProviderDirect {
		runner := s.Runner
		if runner == nil {
			runner = ExecRunner{}
		}
		if err := Upgrade(ctx, status.Provider, runner); err != nil {
			return status, err
		}
		status.Applied = true
		prepared.Status = status
		return status, nil
	}
	if prepared.staged == nil {
		return status, errors.New("direct update was not staged")
	}
	target := s.Executable
	if target == "" {
		var err error
		target, err = os.Executable()
		if err != nil {
			return status, err
		}
	}
	apply, err := ApplyCandidateWithRestart(ctx, prepared.staged.BinaryPath, target, status.Latest, restartArgs)
	if err != nil {
		return status, err
	}
	status.Applied = !apply.Deferred
	status.Deferred = apply.Deferred
	prepared.Status = status
	if !apply.Deferred {
		_ = prepared.Cleanup()
	}
	return status, nil
}

func (s Service) Update(ctx context.Context, current string) (Status, error) {
	prepared, err := s.Prepare(ctx, current)
	if err != nil {
		return Status{}, err
	}
	defer func() {
		if !prepared.Status.Deferred {
			_ = prepared.Cleanup()
		}
	}()
	return s.ApplyPrepared(ctx, &prepared)
}

type ApplyResult struct {
	Deferred bool
}
