//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package kerberos

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func defaultKinitPasswordRunner(timeout time.Duration, principal string, env map[string]string, password string) (commandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, kinitCommand, principal) // #nosec G204 -- principal is a configured Kerberos principal, command name is fixed.
	if env != nil {
		cmd.Env = envSlice(env)
	}
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return commandResult{}, err
	}

	// The PTY starts with terminal echo enabled on common Unix systems. Disable
	// it before the password is ever written so the secret cannot be reflected
	// back into the output capture. MakeRaw also preserves the required TTY
	// contract while avoiding echo/canonical buffering surprises.
	oldState, err := term.MakeRaw(int(ptmx.Fd()))
	if err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return commandResult{}, err
	}
	defer func() { _ = term.Restore(int(ptmx.Fd()), oldState) }()

	done := make(chan string, 1)
	go func() {
		var output bytes.Buffer
		_, _ = io.Copy(&output, ptmx)
		done <- output.String()
	}()

	if _, err := io.WriteString(ptmx, password+"\n"); err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		select {
		case output := <-done:
			return commandResult{Stdout: output, Stderr: output}, err
		case <-time.After(time.Second):
			return commandResult{}, errors.New("timed out draining kinit PTY after password write failure")
		}
	}

	err = cmd.Wait()
	_ = term.Restore(int(ptmx.Fd()), oldState)
	_ = ptmx.Close()
	var output string
	select {
	case output = <-done:
	case <-time.After(time.Second):
		return commandResult{}, errors.New("timed out draining kinit PTY")
	}

	result := commandResult{Stdout: output, Stderr: output}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, context.DeadlineExceeded
	}
	if ee := new(exec.ExitError); errors.As(err, &ee) {
		result.ExitCode = ee.ExitCode()
		return result, nil
	}
	if err != nil {
		return result, err
	}
	return result, nil
}
