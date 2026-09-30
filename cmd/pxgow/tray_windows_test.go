//go:build windows

package main

import (
	"reflect"
	"testing"
)

func TestControlArgsReuseExistingConfigAndPort(t *testing.T) {
	args := []string{"--background", "--config=C:\\Users\\me\\pxgo.ini", "--port=43128", "--proxy=DIRECT"}
	got := controlArgs(args, "--quit")
	want := []string{"--quit", "--config=C:\\Users\\me\\pxgo.ini", "--port=43128"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("controlArgs()=%q want %q", got, want)
	}
}

func TestControlArgsDoNotCreateBackgroundConfig(t *testing.T) {
	got := controlArgs([]string{"--background", "--proxy=DIRECT"}, "--quit")
	want := []string{"--quit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("controlArgs()=%q want %q", got, want)
	}
}
