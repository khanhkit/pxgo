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

func (s Service) Update(ctx context.Context, current string) (Status, error) {
	status, err := s.Check(ctx, current)
	if err != nil {
		return Status{}, err
	}
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
		return status, nil
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
		return status, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = staged.Cleanup()
		}
	}()

	target := s.Executable
	if target == "" {
		target, err = os.Executable()
		if err != nil {
			return status, err
		}
	}
	apply, err := ApplyCandidate(ctx, staged.BinaryPath, target, status.Latest)
	if err != nil {
		return status, err
	}
	status.Applied = !apply.Deferred
	status.Deferred = apply.Deferred
	if apply.Deferred {
		cleanup = false
	}
	return status, nil
}

type ApplyResult struct {
	Deferred bool
}
