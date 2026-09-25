//go:build windows

package main

import "testing"

func TestRaiseNofileLimitWindowsNoop(t *testing.T) {
	raiseNofileLimitBestEffort()
}
