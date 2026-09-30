package background

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHandshakeReady(t *testing.T) {
	launcher, err := NewLauncher()
	if err != nil {
		t.Fatal(err)
	}
	env := launcher.Env()
	if len(env) != 2 {
		t.Fatalf("env=%v", env)
	}
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("bad env %q", entry)
		}
		t.Setenv(name, value)
	}
	reporter, ok, err := ReporterFromEnv()
	if err != nil || !ok {
		t.Fatalf("reporter ok=%v err=%v", ok, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { _ = reporter.Ready(ctx) }()
	if err := launcher.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeError(t *testing.T) {
	launcher, err := NewLauncher()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range launcher.Env() {
		name, value, _ := strings.Cut(entry, "=")
		t.Setenv(name, value)
	}
	reporter, ok, err := ReporterFromEnv()
	if err != nil || !ok {
		t.Fatalf("reporter ok=%v err=%v", ok, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { _ = reporter.Error(ctx, errors.New("bind failed\nsecret detail")) }()
	err = launcher.Wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "bind failed secret detail") {
		t.Fatalf("err=%v", err)
	}
}

func TestReporterRejectsNonLoopback(t *testing.T) {
	t.Setenv(envAddr, "8.8.8.8:1234")
	t.Setenv(envToken, strings.Repeat("a", 64))
	if _, _, err := ReporterFromEnv(); err == nil {
		t.Fatal("expected non-loopback rejection")
	}
}
