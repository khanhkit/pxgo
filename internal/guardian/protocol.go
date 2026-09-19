package guardian

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxMessageBytes = 1024
	ioDeadline      = 5 * time.Second
)

var (
	ErrProtocol       = errors.New("guardian protocol error")
	ErrAuthentication = errors.New("guardian authentication failed")
)

type MessageType string

const (
	MessageReady MessageType = "READY"
	MessageBeat  MessageType = "BEAT"
	MessageStop  MessageType = "STOP"
)

type Message struct {
	Type     MessageType
	Sequence uint64
}

type ControlListener struct {
	listener *net.TCPListener
	token    string
	closeMu  sync.Mutex
	closed   bool
}

type Session struct {
	conn net.Conn
	read *bufio.Reader
	mu   sync.Mutex
}

func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func Listen() (*ControlListener, error) {
	token, err := NewToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return nil, err
	}
	return &ControlListener{listener: listener, token: token}, nil
}

func (c *ControlListener) Addr() string {
	if c == nil || c.listener == nil {
		return ""
	}
	return c.listener.Addr().String()
}

func (c *ControlListener) Token() string {
	if c == nil {
		return ""
	}
	return c.token
}

func (c *ControlListener) Close() error {
	if c == nil {
		return nil
	}
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.listener == nil {
		return nil
	}
	return c.listener.Close()
}

func (c *ControlListener) Accept(ctx context.Context) (*Session, error) {
	if c == nil || c.listener == nil {
		return nil, errors.New("guardian listener unavailable")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(250 * time.Millisecond)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if err := c.listener.SetDeadline(deadline); err != nil {
			return nil, err
		}
		conn, err := c.listener.AcceptTCP()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}

		session := newSession(conn)
		line, err := session.readLine(ctx)
		if err != nil || validateHello(line, c.token) != nil {
			_ = session.Close()
			continue
		}
		if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			_ = session.Close()
			return nil, err
		}
		return session, nil
	}
}

func Connect(ctx context.Context, addr, token string) (*Session, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp4", addr)
	if err != nil {
		return nil, err
	}
	session := newSession(conn)
	if err := session.writeLine(ctx, "HELLO "+token); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func newSession(conn net.Conn) *Session {
	return &Session{conn: conn, read: bufio.NewReaderSize(conn, maxMessageBytes)}
}

func (s *Session) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func (s *Session) Send(ctx context.Context, msg Message) error {
	var line string
	switch msg.Type {
	case MessageReady:
		line = string(MessageReady)
	case MessageBeat:
		line = string(MessageBeat) + " " + strconv.FormatUint(msg.Sequence, 10)
	case MessageStop:
		line = string(MessageStop)
	default:
		return ErrProtocol
	}
	return s.writeLine(ctx, line)
}

func (s *Session) Read(ctx context.Context) (Message, error) {
	line, err := s.readLine(ctx)
	if err != nil {
		return Message{}, err
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return Message{}, ErrProtocol
	}
	switch MessageType(parts[0]) {
	case MessageReady:
		if len(parts) != 1 {
			return Message{}, ErrProtocol
		}
		return Message{Type: MessageReady}, nil
	case MessageStop:
		if len(parts) != 1 {
			return Message{}, ErrProtocol
		}
		return Message{Type: MessageStop}, nil
	case MessageBeat:
		if len(parts) != 2 {
			return Message{}, ErrProtocol
		}
		sequence, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return Message{}, ErrProtocol
		}
		return Message{Type: MessageBeat, Sequence: sequence}, nil
	default:
		return Message{}, ErrProtocol
	}
}

func validateHello(line, expectedToken string) error {
	parts := strings.Fields(line)
	if len(parts) != 2 || parts[0] != "HELLO" || expectedToken == "" || parts[1] != expectedToken {
		return ErrAuthentication
	}
	return nil
}

func (s *Session) readLine(ctx context.Context) (string, error) {
	if s == nil || s.conn == nil || s.read == nil {
		return "", ErrProtocol
	}
	if err := s.conn.SetReadDeadline(operationDeadline(ctx)); err != nil {
		return "", err
	}
	line, err := s.read.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxMessageBytes {
		return "", ErrProtocol
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(line), "\r\n"), nil
}

func (s *Session) writeLine(ctx context.Context, line string) error {
	if s == nil || s.conn == nil || len(line)+1 > maxMessageBytes {
		return ErrProtocol
	}
	if err := s.conn.SetWriteDeadline(operationDeadline(ctx)); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data := []byte(line + "\n")
	written, err := s.conn.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return fmt.Errorf("%w: short write", ErrProtocol)
	}
	return nil
}

func operationDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(ioDeadline)
	if ctx != nil {
		if candidate, ok := ctx.Deadline(); ok && candidate.Before(deadline) {
			deadline = candidate
		}
	}
	return deadline
}
