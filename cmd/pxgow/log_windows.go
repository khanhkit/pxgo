//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func backgroundLogPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(dir, "pxgo", "background-startup.log"), nil
}

func openBackgroundLog() (*os.File, error) {
	path, err := backgroundLogPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func logEarlyError(err error) {
	if err == nil {
		return
	}
	f, openErr := openBackgroundLog()
	if openErr != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), err.Error())
}

func backgroundLogWriter() (io.WriteCloser, error) { return openBackgroundLog() }
