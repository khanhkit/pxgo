package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	stateFileName     = "update-state.json"
	ResultCurrent     = "current"
	ResultAvailable   = "available"
	ResultPrepared    = "prepared"
	ResultApplied     = "applied"
	ResultDeferred    = "deferred"
	ResultCheckFailed = "check_failed"
	ResultApplyFailed = "apply_failed"
)

type State struct {
	Current    string    `json:"current,omitempty"`
	Latest     string    `json:"latest,omitempty"`
	Available  bool      `json:"available"`
	Provider   Provider  `json:"provider,omitempty"`
	Channel    Channel   `json:"channel,omitempty"`
	LastCheck  time.Time `json:"last_check,omitempty"`
	LastResult string    `json:"last_result,omitempty"`
}

func StatePath(configDir string) string {
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, stateFileName)
}

func StateFromStatus(status Status, result string, now time.Time) State {
	if now.IsZero() {
		now = time.Now()
	}
	return State{
		Current:    status.Current,
		Latest:     status.Latest,
		Available:  status.Available,
		Provider:   status.Provider,
		Channel:    status.Channel,
		LastCheck:  now.UTC(),
		LastResult: result,
	}
}

func WriteState(path string, state State) error {
	if path == "" {
		return errors.New("empty update state path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-state-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceStateFile(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func ReadState(path string) (State, error) {
	if path == "" {
		return State{}, errors.New("empty update state path")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- update state path is an internal config-dir path.
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode update state: %w", err)
	}
	return state, nil
}
