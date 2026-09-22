package main

import (
	"os"
	"testing"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/guardian"
)

func TestTCGUARDPARENT026OneShotClassification(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
	}{
		{name: "help", cfg: config.Config{Help: true}},
		{name: "version", cfg: config.Config{Version: true}},
		{name: "save", cfg: config.Config{Save: true}},
		{name: "install", cfg: config.Config{Install: true}},
		{name: "uninstall", cfg: config.Config{Uninstall: true}},
		{name: "password", cfg: config.Config{PasswordAction: true}},
		{name: "client-password", cfg: config.Config{ClientPasswordAction: true}},
		{name: "doctor", cfg: config.Config{Doctor: true}},
		{name: "quit", cfg: config.Config{Quit: true}},
		{name: "self-test", cfg: config.Config{Test: "http://example.test"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isOneShotConfig(tc.cfg) {
				t.Fatalf("%s was not classified as one-shot", tc.name)
			}
		})
	}
	if isOneShotConfig(config.Config{Restart: true}) {
		t.Fatal("restart must continue into a fresh long-running Guardian after quitting the old process")
	}
	if isOneShotConfig(config.Config{}) {
		t.Fatal("ordinary long-running mode classified as one-shot")
	}
}

func TestTCGUARDPARENT025PublicLongRunningDispatchesGuardian(t *testing.T) {
	oldArgs := os.Args
	oldParent := runGuardianParentFunc
	oldWorker := runGuardianWorkerFunc
	t.Cleanup(func() {
		os.Args = oldArgs
		runGuardianParentFunc = oldParent
		runGuardianWorkerFunc = oldWorker
	})

	t.Setenv("PXGOINT_GUARDIAN_ADDR", "")
	t.Setenv("PXGOINT_GUARDIAN_TOKEN", "")
	os.Args = []string{oldArgs[0], "--port=31399"}

	parentCalls := 0
	workerCalls := 0
	runGuardianParentFunc = func(config.Config) int {
		parentCalls++
		return 0
	}
	runGuardianWorkerFunc = func(config.Config, guardian.WorkerControl) int {
		workerCalls++
		return 0
	}
	if code := run(); code != 0 {
		t.Fatalf("run exit=%d", code)
	}
	if parentCalls != 1 || workerCalls != 0 {
		t.Fatalf("parent calls=%d worker calls=%d", parentCalls, workerCalls)
	}
}

func TestTCGUARDPARENT032InternalControlEnvDispatchesWorkerOnly(t *testing.T) {
	oldArgs := os.Args
	oldParent := runGuardianParentFunc
	oldWorker := runGuardianWorkerFunc
	t.Cleanup(func() {
		os.Args = oldArgs
		runGuardianParentFunc = oldParent
		runGuardianWorkerFunc = oldWorker
	})

	t.Setenv("PXGOINT_GUARDIAN_ADDR", "127.0.0.1:43210")
	t.Setenv("PXGOINT_GUARDIAN_TOKEN", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	os.Args = []string{oldArgs[0], "--restart", "--port=31398"}

	parentCalls := 0
	workerCalls := 0
	runGuardianParentFunc = func(config.Config) int {
		parentCalls++
		return 0
	}
	runGuardianWorkerFunc = func(_ config.Config, control guardian.WorkerControl) int {
		workerCalls++
		if control.Addr != "127.0.0.1:43210" {
			t.Fatalf("worker control addr=%q", control.Addr)
		}
		return 0
	}
	if code := run(); code != 0 {
		t.Fatalf("run exit=%d", code)
	}
	if parentCalls != 0 || workerCalls != 1 {
		t.Fatalf("parent calls=%d worker calls=%d", parentCalls, workerCalls)
	}
}
