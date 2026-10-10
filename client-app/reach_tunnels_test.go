package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"sync"
	"testing"
	"time"
)

// maskFrame builds a raw masked client WS frame (the shape a browser sends),
// used to feed relayClientFrames as "the page's own bytes".
func maskFrame(t *testing.T, fin bool, opcode byte, payload []byte) []byte {
	t.Helper()
	b0 := opcode
	if fin {
		b0 |= 0x80
	}
	var header []byte
	n := len(payload)
	switch {
	case n < 126:
		header = []byte{b0, 0x80 | byte(n)}
	case n <= 0xffff:
		header = []byte{b0, 0x80 | 126, 0, 0}
		binary.BigEndian.PutUint16(header[2:], uint16(n))
	default:
		header = []byte{b0, 0x80 | 127, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(header[2:], uint64(n))
	}
	mask := []byte{1, 2, 3, 4}
	out := append([]byte(nil), header...)
	out = append(out, mask...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out
}

// unmaskParseErr decodes one WS frame off r using a reference (independent of
// production code) parser. It never calls t.Fatalf itself, so it is safe to
// call from a goroutine other than the test's own (calling t.Fatalf there
// would runtime.Goexit that goroutine and hang whatever the main goroutine is
// waiting on, instead of failing the test cleanly).
func unmaskParseErr(r *bufio.Reader) (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return
	}
	fin = hdr[0]&0x80 != 0
	opcode = hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	n := int64(hdr[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return
		}
		n = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return
		}
		n = int64(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}

// unmaskParse is unmaskParseErr for the test's own (main) goroutine, where
// t.Fatalf is safe to call directly.
func unmaskParse(t *testing.T, r *bufio.Reader) (fin bool, opcode byte, payload []byte) {
	t.Helper()
	fin, opcode, payload, err := unmaskParseErr(r)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return
}

func TestRelayForwardsFramesByteIdentical(t *testing.T) {
	dst := &lockedBuffer{}
	inj := newWSInjector(dst)
	clientBytes := append(maskFrame(t, true, 0x1, []byte("hello")), maskFrame(t, true, 0x2, bytes.Repeat([]byte{0x42}, 200))...)
	src := bufio.NewReader(bytes.NewReader(clientBytes))

	if err := relayClientFrames(src, inj); err != nil && err != io.EOF {
		t.Fatalf("relayClientFrames: %v", err)
	}

	r := bufio.NewReader(bytes.NewReader(dst.Bytes()))
	fin, op, payload := unmaskParse(t, r)
	if !fin || op != 0x1 || string(payload) != "hello" {
		t.Fatalf("frame 1: fin=%v op=%d payload=%q", fin, op, payload)
	}
	fin, op, payload = unmaskParse(t, r)
	if !fin || op != 0x2 || !bytes.Equal(payload, bytes.Repeat([]byte{0x42}, 200)) {
		t.Fatalf("frame 2 mismatch: fin=%v op=%d len=%d", fin, op, len(payload))
	}
}

// TestRelayHandlesFragmentedMessageAndMidMsgState feeds relayClientFrames a
// message split into two fragments with a ping in between (legal per RFC
// 6455), pacing the source with an io.Pipe it controls so it can inspect
// inj.midMsg in the gap — at that instant relayClientFrames is blocked
// reading the NEXT frame's header from src, not holding inj.mu, so this does
// not race or deadlock with the relay goroutine (its destination, a plain
// bytes.Buffer, never blocks on Write the way a real socket could).
func TestRelayHandlesFragmentedMessageAndMidMsgState(t *testing.T) {
	pr, pw := io.Pipe()
	dst := &lockedBuffer{}
	inj := newWSInjector(dst)
	src := bufio.NewReader(pr)

	part1 := maskFrame(t, false, 0x1, []byte("part1"))
	ping := maskFrame(t, true, 0x9, []byte("ping"))
	part2 := maskFrame(t, true, 0x0, []byte("part2"))

	relayDone := make(chan error, 1)
	go func() { relayDone <- relayClientFrames(src, inj) }()

	if _, err := pw.Write(part1); err != nil {
		t.Fatalf("write part1: %v", err)
	}

	// Poll (bounded) until midMsg flips true: relayClientFrames has finished
	// writing part1 downstream and is now blocked reading the next header
	// from src, so this lock never contends with a blocked Write.
	deadline := time.Now().Add(2 * time.Second)
	for {
		inj.mu.Lock()
		mid := inj.midMsg
		inj.mu.Unlock()
		if mid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for midMsg=true after part1")
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := pw.Write(ping); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if _, err := pw.Write(part2); err != nil {
		t.Fatalf("write part2: %v", err)
	}
	pw.Close()

	if err := <-relayDone; err != nil && err != io.EOF && err != io.ErrClosedPipe {
		t.Fatalf("relayClientFrames: %v", err)
	}
	inj.mu.Lock()
	finalMid := inj.midMsg
	inj.mu.Unlock()
	if finalMid {
		t.Fatalf("expected midMsg=false after the final fragment")
	}

	r := bufio.NewReader(bytes.NewReader(dst.Bytes()))
	if _, op, payload, err := unmaskParseErr(r); err != nil || op != 0x1 || string(payload) != "part1" {
		t.Fatalf("frame 1: op=%d payload=%q err=%v", op, payload, err)
	}
	if _, op, payload, err := unmaskParseErr(r); err != nil || op != 0x9 || string(payload) != "ping" {
		t.Fatalf("frame 2: op=%d payload=%q err=%v", op, payload, err)
	}
	if _, op, payload, err := unmaskParseErr(r); err != nil || op != 0x0 || string(payload) != "part2" {
		t.Fatalf("frame 3: op=%d payload=%q err=%v", op, payload, err)
	}
}

// lockedBuffer is a mutex-guarded bytes.Buffer: relayClientFrames writes to
// it from its own goroutine while the test reads it only after that
// goroutine is known to have stopped, but the guard keeps `go test -race`
// happy about the handoff.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func TestRelayMarksCloseAndUnmaskedDisabled(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		inj := newWSInjector(&lockedBuffer{})
		src := bufio.NewReader(bytes.NewReader(maskFrame(t, true, 0x8, nil)))
		if err := relayClientFrames(src, inj); err != nil {
			t.Fatalf("relayClientFrames: %v", err)
		}
		if !inj.closed {
			t.Fatalf("expected closed=true after a close frame")
		}
	})
	t.Run("unmasked", func(t *testing.T) {
		inj := newWSInjector(&lockedBuffer{})
		// An unmasked frame (mask bit unset): not a conforming client frame.
		frame := []byte{0x81, 0x05, 'h', 'e', 'l', 'l', 'o'}
		src := bufio.NewReader(bytes.NewReader(frame))
		relayClientFrames(src, inj)
		if !inj.disabled {
			t.Fatalf("expected disabled=true after an unmasked frame")
		}
	})
}

func TestInjectAtBoundaryWritesImmediately(t *testing.T) {
	var buf bytes.Buffer
	inj := newWSInjector(&buf)
	if err := inj.Inject([]byte(`{"type":"type_text"}`)); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	r := bufio.NewReader(&buf)
	fin, op, payload := unmaskParse(t, r)
	if !fin || op != 0x1 || string(payload) != `{"type":"type_text"}` {
		t.Fatalf("injected frame mismatch: fin=%v op=%d payload=%q", fin, op, payload)
	}
}

func TestInjectTwiceUsesDifferentMasks(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	newWSInjector(&buf1).Inject([]byte("same payload"))
	newWSInjector(&buf2).Inject([]byte("same payload"))
	if bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Fatalf("two injected frames of the same payload must differ (random mask)")
	}
}

func TestInjectWaitsForMidMsgThenTimesOutBusy(t *testing.T) {
	var buf bytes.Buffer
	inj := newWSInjector(&buf)
	inj.midMsg = true
	fakeNow := time.Unix(1000, 0)
	inj.nowFn = func() time.Time { return fakeNow }
	inj.sleepFn = func(d time.Duration) { fakeNow = fakeNow.Add(d) }
	if err := inj.Inject([]byte("x")); err != errInjectorBusy {
		t.Fatalf("Inject while midMsg forever = %v, want errInjectorBusy", err)
	}
}

func TestInjectAfterCloseOrDisabled(t *testing.T) {
	var buf bytes.Buffer
	closedInj := newWSInjector(&buf)
	closedInj.closed = true
	if err := closedInj.Inject([]byte("x")); err != errInjectorClosed {
		t.Fatalf("Inject after close = %v, want errInjectorClosed", err)
	}
	disabledInj := newWSInjector(&buf)
	disabledInj.disabled = true
	if err := disabledInj.Inject([]byte("x")); err != errInjectorDisabled {
		t.Fatalf("Inject while disabled = %v, want errInjectorDisabled", err)
	}
}

func TestReachTunnelsRegisterLookupAttach(t *testing.T) {
	tuns := newReachTunnels()
	tuns.register("tok1", "https://a.example", 5, 42)
	tun, inj, ok := tuns.lookup("tok1")
	if !ok || tun.origin != "https://a.example" || tun.tenant != 5 || inj != nil {
		t.Fatalf("lookup before attach = %+v inj=%v ok=%v", tun, inj, ok)
	}
	fakeInj := newWSInjector(&bytes.Buffer{})
	detach := tuns.attach("tok1", fakeInj)
	_, inj2, _ := tuns.lookup("tok1")
	if inj2 != fakeInj {
		t.Fatalf("lookup after attach did not return the injector")
	}
	detach()
	_, inj3, _ := tuns.lookup("tok1")
	if inj3 != nil {
		t.Fatalf("lookup after detach should have no injector")
	}
	if _, _, ok := tuns.lookup("unknown"); ok {
		t.Fatalf("lookup of an unregistered token must fail")
	}
}

func TestReachTunnelsEvictsOldestPastCap(t *testing.T) {
	tuns := newReachTunnels()
	for i := 0; i < reachTunnelMax+10; i++ {
		tuns.register(string(rune('a'+i%26))+string(rune('A'+i/26)), "https://x", 1, int64(i))
	}
	if len(tuns.byToken) > reachTunnelMax {
		t.Fatalf("registry grew past cap: %d entries", len(tuns.byToken))
	}
}
