package background

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	envAddr  = "PXGOINT_BACKGROUND_ADDR"
	envToken = "PXGOINT_BACKGROUND_TOKEN" // #nosec G101 -- environment variable name, not a credential.
)

type Launcher struct {
	listener *net.TCPListener
	token    string
	once     sync.Once
}

type Reporter struct {
	addr  string
	token string
}

func NewLauncher() (*Launcher, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return nil, err
	}
	return &Launcher{listener: listener, token: hex.EncodeToString(tokenBytes)}, nil
}

func (l *Launcher) Env() []string {
	if l == nil || l.listener == nil {
		return nil
	}
	return []string{envAddr + "=" + l.listener.Addr().String(), envToken + "=" + l.token}
}

func (l *Launcher) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	var err error
	l.once.Do(func() { err = l.listener.Close() })
	return err
}

func (l *Launcher) Wait(ctx context.Context) error {
	if l == nil || l.listener == nil {
		return errors.New("background launcher unavailable")
	}
	defer l.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		deadline := time.Now().Add(250 * time.Millisecond)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if err := l.listener.SetDeadline(deadline); err != nil {
			return err
		}
		conn, err := l.listener.AcceptTCP()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		status, err := readStatus(ctx, conn, l.token)
		_ = conn.Close()
		if err != nil {
			continue
		}
		if status == "READY" {
			return nil
		}
		if strings.HasPrefix(status, "ERROR ") {
			return errors.New(strings.TrimSpace(strings.TrimPrefix(status, "ERROR ")))
		}
	}
}

func ReporterFromEnv() (Reporter, bool, error) {
	addr := strings.TrimSpace(getenv(envAddr))
	token := strings.TrimSpace(getenv(envToken))
	if addr == "" && token == "" {
		return Reporter{}, false, nil
	}
	if addr == "" || token == "" || len(token) != 64 {
		return Reporter{}, false, errors.New("background launch environment invalid")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return Reporter{}, false, errors.New("background launch address invalid")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return Reporter{}, false, errors.New("background launch address must be loopback")
	}
	return Reporter{addr: addr, token: token}, true, nil
}

func (r Reporter) Ready(ctx context.Context) error { return r.send(ctx, "READY") }
func (r Reporter) Error(ctx context.Context, err error) error {
	message := "background startup failed"
	if err != nil {
		message = strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", " "), "\n", " ")
	}
	return r.send(ctx, "ERROR "+message)
}

func (r Reporter) send(ctx context.Context, status string) error {
	if r.addr == "" || r.token == "" {
		return nil
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp4", r.addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(d)
	}
	_, err = fmt.Fprintf(conn, "HELLO %s\n%s\n", r.token, status)
	return err
}

func readStatus(ctx context.Context, conn net.Conn, token string) (string, error) {
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(d)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() || scanner.Text() != "HELLO "+token {
		return "", errors.New("background launch authentication failed")
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", errors.New("background launch status missing")
	}
	status := scanner.Text()
	if status != "READY" && !strings.HasPrefix(status, "ERROR ") {
		return "", errors.New("background launch status invalid")
	}
	return status, nil
}

var getenv = os.Getenv
