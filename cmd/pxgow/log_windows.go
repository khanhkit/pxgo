//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func logEarlyError(err error) {
	if err == nil {
		return
	}
	dir, dirErr := os.UserConfigDir()
	if dirErr != nil || dir == "" {
		return
	}
	dir = filepath.Join(dir, "pxgo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, "background-startup.log")
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if openErr != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), err.Error())
}
