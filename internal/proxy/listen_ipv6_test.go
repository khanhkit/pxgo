package proxy

import (
	"reflect"
	"testing"

	"github.com/pavelsimo/pxgo/internal/config"
)

func TestListenAddressesUseCanonicalHostPort(t *testing.T) {
	cfg := config.Default()
	cfg.Listen = "127.0.0.1,::1"
	cfg.Port = 3128
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.ListenAddr(), "127.0.0.1:3128"; got != want {
		t.Fatalf("ListenAddr()=%q want %q", got, want)
	}
	if got, want := s.ListenAddrs(), []string{"127.0.0.1:3128", "[::1]:3128"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListenAddrs()=%#v want %#v", got, want)
	}
}
