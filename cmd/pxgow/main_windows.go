//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// pxgow is intentionally tiny: it launches the sibling console runtime without
// allocating a console of its own. Guardian ownership remains in pxgo.exe.
func main() {
	if err := run(); err != nil {
		logEarlyError(err)
		os.Exit(1)
	}
}

func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	pxgo := filepath.Join(filepath.Dir(exe), "pxgo.exe")
	cmd := exec.Command(pxgo, os.Args[1:]...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := configureWindowlessChild(cmd); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
