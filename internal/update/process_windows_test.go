//go:build windows

package update

import (
	"os/exec"
	"testing"
)

func TestConfigureHiddenProcessUsesNoWindow(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	configureHiddenProcess(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("missing Windows process attributes")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("update helper is not hidden")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("creation flags=%#x missing CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
}
