//go:build windows

package proxy

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Windows native regression contract only: TCPConn.SetReadBuffer/SetWriteBuffer
// must succeed and not corrupt bidirectional data delivery. NOT a Windows
// socket-memory upper bound and NOT evidence of Windows 2560 CONNECT throughput.
func TestR148NativeWinSockConfiguredBufferDeliversPayload(t *testing.T) {
	const connections = 8
	const bytesPerConn = 1 << 20
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var wg sync.WaitGroup
	errors := make(chan error, 2*connections)
	for i := 0; i < connections; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := listener.AcceptTCP()
			if e != nil {
				errors <- e
				return
			}
			defer c.Close()
			if e = c.SetReadBuffer(64 << 10); e != nil {
				errors <- e
				return
			}
			if e = c.SetWriteBuffer(64 << 10); e != nil {
				errors <- e
				return
			}
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			received, e := io.CopyN(io.Discard, c, bytesPerConn)
			if e != nil || received != bytesPerConn {
				errors <- io.ErrUnexpectedEOF
				return
			}
			_, e = c.Write([]byte("OK"))
			if e != nil {
				errors <- e
			}
		}()
	}
	payload := bytes.Repeat([]byte{0xa5}, bytesPerConn)
	for i := 0; i < connections; i++ {
		c, e := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
		if e != nil {
			t.Fatal(e)
		}
		if e = c.SetReadBuffer(64 << 10); e != nil {
			t.Fatal(e)
		}
		if e = c.SetWriteBuffer(64 << 10); e != nil {
			t.Fatal(e)
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		if _, e = c.Write(payload); e != nil {
			t.Fatal(e)
		}
		ack := make([]byte, 2)
		if _, e = io.ReadFull(c, ack); e != nil {
			t.Fatal(e)
		}
		if string(ack) != "OK" {
			t.Fatalf("ack corrupted: %q", ack)
		}
		_ = c.Close()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	t.Logf("R148_WINDOWS_BUFFER_RESULT native_winsock=true sockets=%d bytes_delivered=%d configured_read_write_buffer_bytes=65536 reliable_delivery=true", 2*connections, connections*bytesPerConn)
}
