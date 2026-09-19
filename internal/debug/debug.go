package debug

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	runtimedebug "runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pavelsimo/pxgo/internal/diagnostic"
)

const (
	maxLogBytes   = 4 << 20
	maxLogBackups = 3
	logFileMode   = 0o600
)

type Debug struct {
	mu     sync.Mutex
	name   string
	file   *os.File
	stdout io.Writer
}

var (
	instance    atomic.Pointer[Debug]
	lifecycleMu sync.Mutex
)

func Pprint(objs ...any) {
	defer func() { _ = recover() }()
	fmt.Print(diagnostic.RedactText(fmt.Sprintln(objs...)))
}

func LogPanic(logPath string, recovered any) {
	msg := diagnostic.RedactText(fmt.Sprintf("\nPanic: %v\n%s", recovered, runtimedebug.Stack()))
	if d := instance.Load(); d != nil {
		if _, err := d.Write([]byte(msg)); err == nil {
			d.sync() // panic forensics should reach disk when the sink is healthy
			return
		}
	}
	_, _ = os.Stderr.Write([]byte(msg))
	if logPath != "" {
		if err := os.WriteFile(logPath, []byte(msg), logFileMode); err == nil {
			_ = os.Chmod(logPath, logFileMode)
		}
	}
}

func New(name string, appendMode bool) (*Debug, error) {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()

	var nextFile *os.File
	if name != "" {
		var err error
		nextFile, err = openLogFile(name, !appendMode)
		if err != nil {
			return instance.Load(), err
		}
	}

	d := instance.Load()
	created := d == nil
	if created {
		d = &Debug{stdout: os.Stdout}
	}

	d.mu.Lock()
	oldFile := d.file
	d.name = name
	d.file = nextFile
	if d.stdout == nil {
		d.stdout = os.Stdout
	}
	d.mu.Unlock()

	if oldFile != nil {
		_ = oldFile.Close()
	}
	if created {
		instance.Store(d)
	}
	return d, nil
}

func Instance() *Debug {
	return instance.Load()
}

func ResetForTest() {
	lifecycleMu.Lock()
	d := instance.Swap(nil)
	if d != nil {
		_ = d.Close()
	}
	lifecycleMu.Unlock()
}

// Reopen reopens the current log in append mode. Explicit truncation is only
// allowed during New(..., appendMode=false), never as a side effect of reopen.
func (d *Debug) Reopen() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.name == "" {
		if d.file != nil {
			_ = d.file.Close()
			d.file = nil
		}
		return nil
	}
	next, err := openLogFile(d.name, false)
	if err != nil {
		return err
	}
	old := d.file
	d.file = next
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func openLogFile(name string, truncate bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if truncate {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(name, flags, logFileMode)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(logFileMode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (d *Debug) openFileLocked(truncate bool) error {
	f, err := openLogFile(d.name, truncate)
	if err != nil {
		return err
	}
	d.file = f
	return nil
}

func (d *Debug) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil {
		return nil
	}
	err := d.file.Close()
	d.file = nil
	return err
}

func (d *Debug) Write(p []byte) (int, error) {
	redacted := []byte(diagnostic.RedactText(string(p)))

	d.mu.Lock()
	defer d.mu.Unlock()

	var writeErr error
	if d.file != nil {
		if err := d.rotateLocked(len(redacted)); err != nil {
			writeErr = errors.Join(writeErr, err)
		}
		if d.file != nil {
			n, err := d.file.Write(redacted)
			if err != nil {
				writeErr = errors.Join(writeErr, err)
			} else if n != len(redacted) {
				writeErr = errors.Join(writeErr, io.ErrShortWrite)
			}
		}
	}
	if d.stdout != nil {
		n, err := d.stdout.Write(redacted)
		if err != nil {
			writeErr = errors.Join(writeErr, err)
		} else if n != len(redacted) {
			writeErr = errors.Join(writeErr, io.ErrShortWrite)
		}
	}
	if writeErr != nil {
		return 0, writeErr
	}
	return len(p), nil
}

func (d *Debug) rotateLocked(incoming int) error {
	if d.file == nil || d.name == "" || incoming <= 0 {
		return nil
	}
	info, err := d.file.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(incoming) <= maxLogBytes {
		return nil
	}
	if err := d.file.Close(); err != nil {
		d.file = nil
		return err
	}
	d.file = nil

	for i := maxLogBackups; i >= 1; i-- {
		dst := d.name + "." + strconv.Itoa(i)
		if i == maxLogBackups {
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				_ = d.openFileLocked(false)
				return err
			}
		}
		src := d.name
		if i > 1 {
			src = d.name + "." + strconv.Itoa(i-1)
		}
		if err := os.Rename(src, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = d.openFileLocked(false)
			return err
		}
	}
	return d.openFileLocked(true)
}

func (d *Debug) sync() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file != nil {
		_ = d.file.Sync()
	}
}

func (d *Debug) Print(msg string) {
	tree := make([]string, 0, 3)
	for i := 2; i < 5; i++ {
		pc, _, _, ok := runtime.Caller(i)
		if !ok {
			break
		}
		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}
		parts := strings.Split(fn.Name(), ".")
		tree = append(tree, parts[len(parts)-1])
	}
	_, _ = fmt.Fprintf(d, "%d: /%s: %s\n", time.Now().Unix(), strings.Join(tree, "/"), msg)
}

func (d *Debug) GetPrint() func(string) {
	return d.Print
}

// Enabled reports whether debug logging is active. Callers with expensive
// message construction should check it (or use Dprintf) so disabled logging
// costs a single atomic load.
func Enabled() bool {
	return instance.Load() != nil
}

func Dprint(msg string) {
	if d := instance.Load(); d != nil {
		d.Print(msg)
	}
}

// Dprintf formats lazily: when logging is disabled the arguments are never
// formatted, keeping hot paths allocation-free.
func Dprintf(format string, args ...any) {
	if d := instance.Load(); d != nil {
		d.Print(fmt.Sprintf(format, args...))
	}
}
