package guardian

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestTCGUARDPROTO001TokenEntropyAndErrorRedaction(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two generated tokens are equal")
	}
	raw, err := hex.DecodeString(a)
	if err != nil {
		t.Fatalf("token is not hex: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("token bytes=%d, want 32", len(raw))
	}
	err = validateHello("HELLO "+b, a)
	if err == nil {
		t.Fatal("wrong token accepted")
	}
	if strings.Contains(err.Error(), a) || strings.Contains(err.Error(), b) {
		t.Fatalf("authentication error leaked token: %v", err)
	}
}

func TestTCGUARDPROTO002ListenerBindsLoopbackOnly(t *testing.T) {
	control, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	host, _, err := net.SplitHostPort(control.Addr())
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || ip.To4() == nil {
		t.Fatalf("control listener bound %q, want IPv4 loopback", control.Addr())
	}
}

func TestTCGUARDPROTO003RejectsRogueThenAcceptsExpectedChild(t *testing.T) {
	control, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	accepted := make(chan *Session, 1)
	errc := make(chan error, 1)
	go func() {
		session, err := control.Accept(ctx)
		if err != nil {
			errc <- err
			return
		}
		accepted <- session
	}()

	rogue, err := net.DialTimeout("tcp", control.Addr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = rogue.Write([]byte("HELLO wrong-token\n"))
	_ = rogue.Close()

	child, err := Connect(ctx, control.Addr(), control.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	select {
	case parent := <-accepted:
		defer parent.Close()
	case err := <-errc:
		t.Fatalf("accept expected child: %v", err)
	case <-ctx.Done():
		t.Fatal("expected child was not accepted")
	}
}

func TestTCGUARDPROTO005BoundedMessageProtocol(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	writer := newSession(left)
	reader := newSession(right)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	go func() {
		_ = writer.Send(ctx, Message{Type: MessageReady})
		_ = writer.Send(ctx, Message{Type: MessageBeat, Sequence: 42})
		_ = writer.Send(ctx, Message{Type: MessageStop})
	}()

	for _, want := range []Message{
		{Type: MessageReady},
		{Type: MessageBeat, Sequence: 42},
		{Type: MessageStop},
	} {
		got, err := reader.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("message=%+v, want %+v", got, want)
		}
	}

	left2, right2 := net.Pipe()
	defer left2.Close()
	defer right2.Close()
	go func() {
		w := bufio.NewWriter(left2)
		_, _ = w.WriteString(strings.Repeat("x", maxMessageBytes+50) + "\n")
		_ = w.Flush()
	}()
	_, err = newSession(right2).Read(ctx)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized message error=%v, want ErrProtocol", err)
	}
	if err != nil && strings.Contains(err.Error(), strings.Repeat("x", 16)) {
		t.Fatalf("protocol error echoed payload: %v", err)
	}
}
