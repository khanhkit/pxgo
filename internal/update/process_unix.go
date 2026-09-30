//go:build !windows

package update

import "os/exec"

func configureHiddenProcess(_ *exec.Cmd) {}
