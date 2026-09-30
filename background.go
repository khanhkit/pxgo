package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/khanhkit/pxgo/internal/background"
	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/diagnostic"
	"github.com/khanhkit/pxgo/internal/winstartup"
)

const backgroundStartupTimeout = 15 * time.Second

func launchBackground(_ config.Config) int {
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "--background is supported on Windows only")
		return 6
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 6
	}
	companion, err := winstartup.BackgroundExecutable(executable)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 6
	}
	if _, err := os.Stat(companion); err != nil {
		fmt.Fprintln(os.Stderr, "background companion unavailable")
		return 6
	}
	launcher, err := background.NewLauncher()
	if err != nil {
		fmt.Fprintln(os.Stderr, "background startup handshake unavailable")
		return 6
	}
	defer launcher.Close()

	args := make([]string, 0, len(os.Args)-1)
	for _, arg := range os.Args[1:] {
		if arg != "--background" {
			args = append(args, arg)
		}
	}
	cmd := exec.Command(companion, args...)
	cmd.Env = append(os.Environ(), launcher.Env()...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "background launch failed")
		return 6
	}
	_ = cmd.Process.Release()

	ctx, cancel := context.WithTimeout(context.Background(), backgroundStartupTimeout)
	defer cancel()
	if err := launcher.Wait(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "background startup timed out")
		} else {
			fmt.Fprintln(os.Stderr, diagnostic.RedactText(err.Error()))
		}
		return 6
	}
	return 0
}
