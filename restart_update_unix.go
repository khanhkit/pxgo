//go:build !windows

package main

import (
	"os"
	"syscall"
)

func restartUpdatedProcess(path string, args []string) error {
	argv := append([]string{path}, args...)
	// #nosec G702 -- path is os.Executable after a verified provider update; no shell or PATH lookup is involved.
	return syscall.Exec(path, argv, os.Environ())
}
