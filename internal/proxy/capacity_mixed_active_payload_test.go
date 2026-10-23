package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Chunked echo avoids the fake full-duplex deadlock of writing a 10 MiB
// message before reading anything back through bounded TCP socket buffers.
func capacityActiveExchange(c net.Conn, index, round, size int) (time.Duration, error) {
	if err := c.SetDeadline(time.Now().Add(18 * time.Second)); err != nil {
		return 0, err
	}
	began := time.Now()
	for sent := 0; sent < size; {
		chunk := min(64<<10, size-sent)
		// Each chunk has position-dependent content, so a replayed or missing
		// chunk cannot be mistaken for another identical zero-filled block.
		payload := make([]byte, chunk)
		for i := range payload {
			payload[i] = byte((index*37 + round*19 + (sent+i)*13) % 251)
		}
		echoed := make([]byte, chunk)
		written, err := c.Write(payload)
		if err != nil {
			return 0, err
		}
		if written != chunk {
			return 0, io.ErrShortWrite
		}
		if _, err := io.ReadFull(c, echoed); err != nil {
			return 0, err
		}
		if !bytes.Equal(payload, echoed) {
			return 0, fmt.Errorf("tunnel=%d round=%d offset=%d payload corrupted", index, round, sent)
		}
		sent += chunk
	}
	return time.Since(began), nil
}

// Independent reader and writer exercise true full-duplex TCP relay under
// window/backpressure, distinct from the smaller request/reply chunked echoes.
// The 10 MiB transfer is bounded by one deadline and verifies every byte.
func capacityActiveFullDuplex(c net.Conn, index, round, size int) (time.Duration, error) {
	if err := c.SetDeadline(time.Now().Add(18 * time.Second)); err != nil {
		return 0, err
	}
	original := make([]byte, size)
	for i := range original {
		original[i] = byte((index*37 + round*19 + i*13) % 251)
	}
	returned := make([]byte, size)
	sent := make(chan error, 1)
	began := time.Now()
	go func() {
		n, err := io.Copy(c, bytes.NewReader(original))
		if err == nil && n != int64(size) {
			err = io.ErrShortWrite
		}
		sent <- err
	}()
	_, readErr := io.ReadFull(c, returned)
	writeErr := <-sent
	if readErr != nil {
		return 0, fmt.Errorf("duplex read: %w", readErr)
	}
	if writeErr != nil {
		return 0, fmt.Errorf("duplex write: %w", writeErr)
	}
	if !bytes.Equal(original, returned) {
		return 0, fmt.Errorf("duplex tunnel=%d round=%d altered %d bytes", index, round, size)
	}
	return time.Since(began), nil
}

func capacityActiveTransferRounds(c net.Conn, index, frameSize, rounds int, transferring *atomic.Int64, samples chan<- time.Duration) error {
	for round := 0; round < rounds; round++ {
		size := frameSize
		if index == 0 && round == 0 {
			size = 10 << 20
		}
		transferring.Add(1)
		var latency time.Duration
		var err error
		if size == 10<<20 {
			latency, err = capacityActiveFullDuplex(c, index, round, size)
		} else {
			latency, err = capacityActiveExchange(c, index, round, size)
		}
		transferring.Add(-1)
		if err != nil {
			return err
		}
		samples <- latency
		time.Sleep(12 * time.Millisecond)
	}
	return c.SetDeadline(time.Time{})
}

func capacityActiveShortHTTP(proxy, origin string, expected []byte) error {
	c, err := net.DialTimeout("tcp", proxy, 6*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err = c.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return err
	}
	if _, err = fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n", origin); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodGet})
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(len(expected)+1)))
	closeErr := response.Body.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, expected) {
		return fmt.Errorf("short payload mismatched status=%d length=%d expected=%d", response.StatusCode, len(body), len(expected))
	}
	return nil
}

// TestCapacityMixedActivePayload runs REAL bidirectional traffic across
// multiple already-established CONNECT tunnels while competing short HTTP
// bodies flow through the same global capacity governor. No mock permit counts.
func TestCapacityMixedActivePayload(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_MIXED_BENCH") != "1" {
		t.Skip("native mixed workload opt-in")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("explicit AUTO required")
	}
	seed, err := strconv.Atoi(os.Getenv("PXGO_CAPACITY_MIX_SEED"))
	if err != nil || seed < 0 || seed > 10000 {
		t.Fatalf("invalid seed: %v", err)
	}
	sizeOptions := []int{1024, 16384, 131072, 262144}
	payloadSize := sizeOptions[seed%len(sizeOptions)]
	shortSize := []int{128, 1024, 16384, 65536}[seed%4]
	heldCount := 48 + seed%4*12
	activeCount := heldCount / 2
	shortCount := 120 + seed%4*16
	const rounds = 8
	httpPayload := bytes.Repeat([]byte{'h'}, shortSize)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(httpPayload) }))
	defer origin.Close()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var serverWG sync.WaitGroup
	serverWG.Add(1)
	go func() {
		defer serverWG.Done()
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			serverWG.Add(1)
			go func(c net.Conn) { defer serverWG.Done(); defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	defer func() { _ = echo.Close(); serverWG.Wait() }()
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.Idle = 45
	cfg.SockTimeout = 20
	px := startTestProxy(t, cfg)
	held := make([]net.Conn, heldCount)
	defer func() {
		for _, c := range held {
			if c != nil {
				_ = c.Close()
			}
		}
	}()
	// Limited outstanding TCP handshakes, leaving Windows ephemeral SYN backlog
	// separate from steady-state gateway pressure.
	type openResult struct {
		index int
		conn  net.Conn
		err   error
	}
	opens := make(chan openResult, heldCount)
	admissionTokens := make(chan struct{}, 12)
	for i := range held {
		go func(i int) {
			admissionTokens <- struct{}{}
			c, e := capacityMixedConnect(px.ListenAddr(), echo.Addr().String(), i)
			<-admissionTokens
			opens <- openResult{i, c, e}
		}(i)
	}
	var firstOpenErr error
	for i := 0; i < heldCount; i++ {
		result := <-opens
		if result.err != nil {
			if firstOpenErr == nil {
				firstOpenErr = result.err
			}
			continue
		}
		held[result.index] = result.conn
	}
	if firstOpenErr != nil {
		t.Fatalf("CONNECT cohort incomplete: %v", firstOpenErr)
	}
	if activeCount == heldCount {
		t.Fatal("must retain genuinely idle and active long tunnels")
	}
	var activeWorkers atomic.Int64
	var activeTransfers atomic.Int64
	var overlapShorts atomic.Int64
	var shortStartsDuringTransfer atomic.Int64
	var shortEndsDuringTransfer atomic.Int64
	longErrors := make(chan error, activeCount)
	longLatency := make(chan time.Duration, activeCount*rounds)
	longStart := make(chan struct{})
	shortStart := make(chan struct{})
	longReady := make(chan struct{}, activeCount)
	var longWG sync.WaitGroup
	for i := 0; i < activeCount; i++ {
		longWG.Add(1)
		go func(index int) {
			defer longWG.Done()
			<-longStart
			c := held[index]
			activeWorkers.Add(1)
			defer activeWorkers.Add(-1)
			longReady <- struct{}{}
			if err := capacityActiveTransferRounds(c, index, payloadSize, rounds, &activeTransfers, longLatency); err != nil {
				longErrors <- err
			}
		}(i)
	}
	shortErrors := make(chan error, shortCount)
	shortLatencies := make(chan time.Duration, shortCount)
	slots := make(chan struct{}, 12)
	var shortWG sync.WaitGroup
	for i := 0; i < shortCount; i++ {
		shortWG.Add(1)
		go func() {
			defer shortWG.Done()
			<-shortStart
			offered := time.Now()
			if activeTransfers.Load() > 0 {
				shortStartsDuringTransfer.Add(1)
			}
			slots <- struct{}{}
			defer func() { <-slots }()
			if err := capacityActiveShortHTTP(px.ListenAddr(), origin.URL, httpPayload); err != nil {
				shortErrors <- err
				return
			}
			if activeWorkers.Load() > 0 {
				overlapShorts.Add(1)
			}
			if activeTransfers.Load() > 0 {
				shortEndsDuringTransfer.Add(1)
			}
			shortLatencies <- time.Since(offered)
		}()
	}
	startAt := time.Now()
	close(longStart)
	for i := 0; i < activeCount; i++ {
		<-longReady
	}
	close(shortStart)
	shortWG.Wait()
	longWG.Wait()
	close(shortErrors)
	close(longErrors)
	close(shortLatencies)
	close(longLatency)
	var firstError error
	for e := range shortErrors {
		if firstError == nil {
			firstError = e
		}
	}
	for e := range longErrors {
		if firstError == nil {
			firstError = e
		}
	}
	shorts := make([]time.Duration, 0, shortCount)
	for v := range shortLatencies {
		shorts = append(shorts, v)
	}
	longs := make([]time.Duration, 0, activeCount*rounds)
	for v := range longLatency {
		longs = append(longs, v)
	}
	if firstError != nil || len(shorts) != shortCount || len(longs) != activeCount*rounds || overlapShorts.Load() == 0 {
		t.Fatalf("mixed ACTIVE payload failed: first=%v short=%d/%d long_samples=%d/%d overlapped_short=%d", firstError, len(shorts), shortCount, len(longs), activeCount*rounds, overlapShorts.Load())
	}
	for i, c := range held {
		if e := verifyCapacityHeldTunnel(c, i); e != nil {
			t.Fatalf("post-active long tunnel %d failed: %v", i, e)
		}
	}
	snap := px.capacityDiagnosticSnapshot()
	if snap.ActiveDownstream != heldCount {
		t.Fatalf("active physical sockets=%d expected %d", snap.ActiveDownstream, heldCount)
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") == "1" {
		cs := snap.ClassPools
		if cs == nil || cs.Long != heldCount || cs.Short != 0 || cs.Provisional != 0 || cs.Total != heldCount {
			t.Fatalf("class conservation failed %+v", cs)
		}
	} else if snap.ClassPools != nil {
		t.Fatalf("class accounting unexpectedly enabled %+v", snap.ClassPools)
	}
	_, sp95, sp99 := capacityLatencySummary(shorts)
	_, lp95, lp99 := capacityLatencySummary(longs)
	totalBytes := int64(activeCount*rounds*payloadSize - payloadSize + (10 << 20))
	t.Logf("ACTIVE_PAYLOAD_RESULT seed=%d held_long=%d streaming_long=%d idle_long=%d streamed_bytes=%d per_round_bytes=%d bulk_bytes=%d short_requests=%d short_body_bytes=%d short_complete=%d short_body_total_bytes=%d overlapped_worker_shorts=%d short_starts_during_actual_transfer=%d short_finishes_during_actual_transfer=%d short_offered_p95=%s short_offered_p99=%s long_roundtrip_p95=%s long_roundtrip_p99=%s physical_active=%d elapsed=%s", seed, heldCount, activeCount, heldCount-activeCount, totalBytes, payloadSize, 10<<20, shortCount, shortSize, len(shorts), shortCount*shortSize, overlapShorts.Load(), shortStartsDuringTransfer.Load(), shortEndsDuringTransfer.Load(), sp95, sp99, lp95, lp99, snap.ActiveDownstream, time.Since(startAt))
	for i, c := range held {
		_ = c.Close()
		held[i] = nil
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := px.capacityDiagnosticSnapshot()
		if s.ActiveDownstream == 0 && (s.ClassPools == nil || s.ClassPools.Total == 0) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("post-payload global/class drain failed %+v", px.capacityDiagnosticSnapshot())
}
