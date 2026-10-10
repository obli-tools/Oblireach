package supportlink

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memChannel is an in-memory ordered, reliable data channel with a
// bufferedAmount that only drains as the receiver consumes messages.
type memChannel struct {
	peer     *memChannel
	q        chan []byte
	buffered atomic.Int64
	maxSeen  atomic.Int64

	mu                        sync.Mutex
	threshold                 uint64
	onMsg                     func([]byte)
	onOpenF, onCloseF, onLowF func()
	closed                    bool
	closeOnce                 sync.Once
}

func memPair() (*memChannel, *memChannel) {
	a := &memChannel{q: make(chan []byte, 1<<16)}
	b := &memChannel{q: make(chan []byte, 1<<16)}
	a.peer, b.peer = b, a
	go a.deliver()
	go b.deliver()
	return a, b
}

// deliver hands a's outgoing messages to the peer's handler.
func (c *memChannel) deliver() {
	for m := range c.q {
		c.peer.mu.Lock()
		f := c.peer.onMsg
		c.peer.mu.Unlock()
		if f != nil {
			f(m)
		}
		n := c.buffered.Add(-int64(len(m)))
		c.mu.Lock()
		low, th := c.onLowF, c.threshold
		c.mu.Unlock()
		if low != nil && uint64(n) <= th && uint64(n+int64(len(m))) > th {
			low()
		}
	}
}

func (c *memChannel) send(b []byte) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return io.ErrClosedPipe
	}
	n := c.buffered.Add(int64(len(b)))
	if n > c.maxSeen.Load() {
		c.maxSeen.Store(n)
	}
	c.q <- append([]byte(nil), b...)
	return nil
}
func (c *memChannel) bufferedAmount() uint64 { return uint64(c.buffered.Load()) }
func (c *memChannel) setBufferedAmountLowThreshold(n uint64) {
	c.mu.Lock()
	c.threshold = n
	c.mu.Unlock()
}
func (c *memChannel) onBufferedAmountLow(f func()) { c.mu.Lock(); c.onLowF = f; c.mu.Unlock() }
func (c *memChannel) onOpen(f func())              { c.mu.Lock(); c.onOpenF = f; c.mu.Unlock() }
func (c *memChannel) onClose(f func())             { c.mu.Lock(); c.onCloseF = f; c.mu.Unlock() }
func (c *memChannel) onMessage(f func([]byte))     { c.mu.Lock(); c.onMsg = f; c.mu.Unlock() }
func (c *memChannel) open() {
	c.mu.Lock()
	f := c.onOpenF
	c.mu.Unlock()
	f()
}
func (c *memChannel) close() error {
	for _, x := range []*memChannel{c, c.peer} {
		x.closeOnce.Do(func() {
			x.mu.Lock()
			x.closed = true
			f := x.onCloseF
			x.mu.Unlock()
			if f != nil {
				go f()
			}
		})
	}
	return nil
}

type memPeer struct{ link *memChannel }

func (p *memPeer) channel() dataChannel               { return p.link }
func (p *memPeer) createOffer() (string, error)       { return "", nil }
func (p *memPeer) acceptOffer(string) (string, error) { return "", nil }
func (p *memPeer) setAnswer(string) error             { return nil }
func (p *memPeer) addCandidate(string) error          { return nil }
func (p *memPeer) onCandidate(func(string))           {}
func (p *memPeer) onFailed(func(string))              {}
func (p *memPeer) path() (Path, string)               { return PathDirect, "mem" }
func (p *memPeer) close() error                       { p.link.close(); return nil }

func memSessions(t *testing.T) (*Session, *Session, *memPeer) {
	t.Helper()
	c1, c2 := memPair()
	p1, p2 := &memPeer{c1}, &memPeer{c2}
	keys := sessionKeys{sas: "123 456"}
	a := newSession("sid", keys, PeerInfo{}, p1)
	b := newSession("sid", keys, PeerInfo{}, p2)
	for _, c := range []*memChannel{c1, c2} {
		c.open()
	}
	for _, s := range []*Session{a, b} {
		if err := s.waitOpen(time.Second); err != nil {
			t.Fatal(err)
		}
	}
	return a, b, p1
}

func TestFramingRoundtrip(t *testing.T) {
	a, b, _ := memSessions(t)
	defer a.Close()
	sizes := []int{0, 1, chunkSize - 2, chunkSize - 1, chunkSize, 3*chunkSize + 7, 5 << 20}
	rnd := rand.New(rand.NewSource(1))
	var want [][]byte
	for _, n := range sizes {
		p := make([]byte, n)
		rnd.Read(p)
		want = append(want, p)
	}
	go func() {
		for i, p := range want {
			if err := a.WriteFrame(OpBinary, p); err != nil {
				t.Error(err)
			}
			if err := a.WriteFrame(OpText, []byte{byte('a' + i)}); err != nil {
				t.Error(err)
			}
			_ = a.WriteFrame(OpPing, nil) // accepted and dropped
		}
	}()
	// One stream: binary and text alternate exactly as written.
	for i := range want {
		op, p, err := b.ReadFrame()
		if err != nil || op != OpBinary || !bytes.Equal(p, want[i]) {
			t.Fatalf("binary frame %d (%d bytes): op 0x%x, %d bytes, %v", i, len(want[i]), op, len(p), err)
		}
		op, p, err = b.ReadFrame()
		if err != nil || op != OpText || string(p) != string(rune('a'+i)) {
			t.Fatalf("text frame %d: op 0x%x %q %v", i, op, p, err)
		}
	}
}

func TestFramingBackpressure(t *testing.T) {
	a, b, p := memSessions(t)
	defer a.Close()
	const total = 24 << 20
	frame := make([]byte, 256<<10)
	go func() {
		for sent := 0; sent < total; sent += len(frame) {
			if err := a.WriteFrame(OpBinary, frame); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	got := 0
	for got < total {
		_, f, err := b.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		got += len(f)
		time.Sleep(200 * time.Microsecond) // slow consumer
	}
	// Writes wait above highWater: the queue never grows past it by more
	// than one chunk.
	if max := p.link.maxSeen.Load(); max > highWater+chunkSize {
		t.Fatalf("buffered amount reached %d bytes, bound is %d", max, highWater+chunkSize)
	}
}

func TestCloseAndEOF(t *testing.T) {
	a, b, _ := memSessions(t)
	if err := a.WriteFrame(OpText, []byte("last")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := a.WriteFrame(OpClose, nil); err != nil {
		t.Fatal(err)
	}
	// The frame sent before the close is still delivered, then EOF.
	op, p, err := b.ReadFrame()
	if err != nil || op != OpText || string(p) != "last" {
		t.Fatalf("got %x %q %v", op, p, err)
	}
	select {
	case <-b.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("peer not closed")
	}
	if _, _, err := b.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
	if err := a.WriteFrame(OpText, []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
	if err := a.WriteFrame(0x3, nil); err == nil {
		t.Fatal("unknown op must be refused")
	}
}

func TestMalformedFrame(t *testing.T) {
	for name, msgs := range map[string][][]byte{
		"unknown op":         {{0x3 | finBit, 'x'}},
		"close op":           {{OpClose | finBit}},
		"reserved bits":      {{OpText | finBit | 0x10, 'x'}},
		"empty message":      {{}},
		"op change in frame": {{OpText, 'x'}, {OpBinary | finBit, 'y'}},
	} {
		t.Run(name, func(t *testing.T) {
			_, b, p := memSessions(t)
			for _, m := range msgs {
				_ = p.link.send(m)
			}
			select {
			case <-b.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("malformed frame must close the session")
			}
			if _, _, err := b.ReadFrame(); err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("want a protocol error, got %v", err)
			}
		})
	}
}

// TestInterleavedOrderMem: text and binary frames of random sizes, both
// directions at once, are read in exactly the order they were written.
func TestInterleavedOrderMem(t *testing.T) {
	a, b, _ := memSessions(t)
	defer a.Close()
	st := checkInterleaved(t, a, b, 800, 1)
	t.Logf("%s", st)
}
