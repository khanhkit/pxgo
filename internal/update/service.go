package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
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
	lock   *updateLock
}

type Service struct {
	Checker         Checker
	Stager          Stager
	Runner          CommandRunner
	Provider        Provider
	Channel         Channel
	Executable      string
	StatePath       string
	GOOS            string
	GOARCH          string
	LookPath        func(string) (string, error)
	VerifyInstalled func(context.Context, string, string) error
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
	ownershipExecutable := executable
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil && resolved != "" {
		ownershipExecutable = resolved
	}
	provider, err := ResolveProvider(configured, ownershipExecutable, goos)
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
	if p == nil {
		return nil
	}
	var errs []error
	if p.staged != nil {
		errs = append(errs, p.staged.Cleanup())
		p.staged = nil
	}
	if p.lock != nil {
		errs = append(errs, p.lock.Close())
		p.lock = nil
	}
	return errors.Join(errs...)
}

func (s Service) Prepare(ctx context.Context, current string) (PreparedUpdate, error) {
	lock, err := acquireUpdateLock(updateLockPath(s.StatePath))
	if err != nil {
		return PreparedUpdate{}, err
	}
	prepared := PreparedUpdate{lock: lock}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = prepared.Cleanup()
		}
	}()

	stagingBase := s.Stager.TempDir
	if stagingBase == "" {
		stagingBase = os.TempDir()
	}
	_ = CleanupStaleStaging(stagingBase, 24*time.Hour, time.Now())
	status, err := s.Check(ctx, current)
	if err != nil {
		return PreparedUpdate{}, err
	}
	prepared.Status = status
	if !status.Available || status.Provider != ProviderDirect {
		keepLock = true
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
	keepLock = true
	return prepared, nil
}

func (s Service) verifyManagedInstall(ctx context.Context, expectedVersion string) error {
	verify := s.VerifyInstalled
	if verify == nil {
		verify = verifyCandidateVersion
	}
	var candidates []string
	if s.Executable != "" {
		candidates = append(candidates, s.Executable)
	} else if executable, err := os.Executable(); err == nil && executable != "" {
		candidates = append(candidates, executable)
	}
	lookPath := s.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if resolved, err := lookPath(productName); err == nil && resolved != "" {
		duplicate := false
		for _, candidate := range candidates {
			if candidate == resolved {
				duplicate = true
				break
			}
		}
		if !duplicate {
			candidates = append(candidates, resolved)
		}
	}
	if len(candidates) == 0 {
		return errors.New("updated PxGo executable could not be resolved")
	}
	var errs []error
	for _, candidate := range candidates {
		if err := verify(ctx, candidate, expectedVersion); err == nil {
			return nil
		} else {
			errs = append(errs, fmt.Errorf("%s: %w", candidate, err))
		}
	}
	return errors.Join(errs...)
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
		if err := s.verifyManagedInstall(ctx, status.Latest); err != nil {
			return status, fmt.Errorf("verify %s update: %w", status.Provider, err)
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
	apply, err := ApplyCandidateWithRestartState(ctx, prepared.staged.BinaryPath, target, status.Latest, restartArgs, s.StatePath, status)
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
