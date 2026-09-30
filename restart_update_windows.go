//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

const updateRestartCreateNoWindow = 0x08000000

func restartUpdatedProcess(path string, args []string) error {
	// #nosec G204 -- path is os.Executable after a verified provider update; argv is inherited from the current PxGo process.
	cmd := exec.Command(path, args...)
	cmd.Dir = filepath.Dir(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: updateRestartCreateNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
