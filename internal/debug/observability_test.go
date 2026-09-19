package debug

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTCOBSLOG001ConcurrentFirstInitReturnsOneInstance(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	logfile := filepath.Join(t.TempDir(), "singleton.log")
	const workers = 64
	start := make(chan struct{})
	results := make(chan *Debug, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, err := New(logfile, true)
			results <- d
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("New: %v", err)
		}
	}
	var first *Debug
	for d := range results {
		if first == nil {
			first = d
			continue
		}
		if d != first {
			t.Fatalf("concurrent New returned multiple instances: %p != %p", d, first)
		}
	}
	if first == nil || Instance() != first {
		t.Fatal("singleton instance not retained")
	}
}

func TestTCOBSLOG002LogFileIsRestrictive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs are not represented by Unix permission bits")
	}
	ResetForTest()
	t.Cleanup(ResetForTest)

	logfile := filepath.Join(t.TempDir(), "private.log")
	d, err := New(logfile, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(logfile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("log permissions = %o, want 600", got)
	}
}

func TestTCOBSLOG003ReopenPreservesExistingRecords(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	logfile := filepath.Join(t.TempDir(), "reopen.log")
	d, err := New(logfile, false)
	if err != nil {
		t.Fatal(err)
	}
	d.Print("first")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Reopen(); err != nil {
		t.Fatal(err)
	}
	d.Print("second")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logfile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "first") || !strings.Contains(text, "second") {
		t.Fatalf("reopen truncated log: %q", text)
	}
}

func TestTCOBSLOG004WritePropagatesSinkFailure(t *testing.T) {
	wantErr := errors.New("sink failed")
	d := &Debug{stdout: failingWriter{err: wantErr}}
	n, err := d.Write([]byte("secret-safe message"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write error = %v, want %v", err, wantErr)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0 from failing sink", n)
	}
}

func TestTCOBSLOG006RotationIsBounded(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	logfile := filepath.Join(t.TempDir(), "rotate.log")
	d, err := New(logfile, false)
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("x", maxLogBytes/2+1)
	for i := 0; i < maxLogBackups+4; i++ {
		if _, err := d.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(logfile + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > maxLogBackups+1 {
		t.Fatalf("rotation produced %d files, max %d: %#v", len(matches), maxLogBackups+1, matches)
	}
}
