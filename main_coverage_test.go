package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelfTestURLModesAndAllMode(t *testing.T) {
	tests := []struct {
		input string
		want  []string
		all   bool
	}{
		{input: selfTestAll, want: []string{selfTestHTTPURL, selfTestHTTPSURL}, all: true},
		{input: "1", want: []string{selfTestHTTPURL, selfTestHTTPSURL}, all: true},
		{input: "all:example.com", want: []string{"http://example.com", "https://example.com"}, all: true},
		{input: "all:https://example.com", want: []string{"https://example.com"}, all: true},
		{input: "https://example.com", want: nil, all: false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := selfTestURLs(tc.input)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("selfTestURLs(%q)=%v want %v", tc.input, got, tc.want)
			}
			if gotAll := selfTestAllMode(tc.input); gotAll != tc.all {
				t.Fatalf("selfTestAllMode(%q)=%v want %v", tc.input, gotAll, tc.all)
			}
		})
	}
}

func TestWaitForRunningProxyAndClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	if err := waitForRunningProxy(addr); err != nil {
		_ = ln.Close()
		t.Fatalf("running listener not detected: %v", err)
	}
	if waitForClosed(addr, 30*time.Millisecond) {
		_ = ln.Close()
		t.Fatal("open listener reported closed")
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if !waitForClosed(addr, time.Second) {
		t.Fatal("closed listener remained reachable")
	}
	if err := waitForRunningProxy(addr); err == nil {
		t.Fatal("closed listener unexpectedly reported running")
	}
}

func TestPrintHelpAndDiagnosticSnapshotPath(t *testing.T) {
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	printHelp()
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	t.Cleanup(func() { os.Stdout = oldStdout })

	data, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatal(err)
	}
	_ = readEnd.Close()
	if text := string(data); !strings.Contains(text, "Kerberos/SPNEGO") || !strings.Contains(text, "--doctor") {
		t.Fatalf("unexpected help output: %s", text)
	}

	path := diagnosticSnapshotPath()
	if path != "" && filepath.Base(path) != "diagnostic-snapshot.json" {
		t.Fatalf("diagnostic snapshot path=%q", path)
	}
}
