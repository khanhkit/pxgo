package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/wproxy"
)

func TestTCWINPACREG006ReloadClosesPreviousResolver(t *testing.T) {
	old, err := wproxy.New(wproxy.ModeAuto, nil, "", "")
	if err != nil {
		t.Fatalf("wproxy.New() error = %v", err)
	}
	s := &Server{
		cfg:        config.Config{ProxyReload: 1},
		w:          old,
		lastReload: time.Now().Add(-2 * time.Second),
		closed:     make(chan struct{}),
	}

	if err := s.reloadProxyIfDue(); err != nil {
		t.Fatalf("reloadProxyIfDue() error = %v", err)
	}

	_, _, _, err = old.FindProxyForURL("https://example.com")
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("old resolver after reload error = %v, want closed resolver", err)
	}
}

func TestTCWINPACREG007ShutdownClosesCurrentResolver(t *testing.T) {
	wp, err := wproxy.New(wproxy.ModeAuto, nil, "", "")
	if err != nil {
		t.Fatalf("wproxy.New() error = %v", err)
	}
	s := &Server{w: wp, closed: make(chan struct{})}

	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	_, _, _, err = wp.FindProxyForURL("https://example.com")
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("resolver after Shutdown error = %v, want closed resolver", err)
	}
}
