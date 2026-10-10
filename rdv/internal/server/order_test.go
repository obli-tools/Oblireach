package server

import (
	"bytes"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	sl "github.com/oblireach/supportlink"
)

// fragSize is supportlink's data channel message size (16 KiB): frame sizes
// around it exercise the fragmentation boundary.
const fragSize = 16 * 1024

type testFrame struct {
	op byte
	p  []byte
}

// genFrames draws n frames mixing text and binary, from empty to over
// 1 MiB, many tiny and many around the fragmentation boundary. Every frame
// carries its index.
func genFrames(seed int64, n int) []testFrame {
	rnd := rand.New(rand.NewSource(seed))
	out := make([]testFrame, n)
	for i := range out {
		var size int
		switch r := rnd.Intn(100); {
		case r < 45:
			size = rnd.Intn(48)
		case r < 70:
			size = 48 + rnd.Intn(4000)
		case r < 85:
			size = fragSize - 4 + rnd.Intn(8)
		case r < 95:
			size = rnd.Intn(5 * fragSize)
		default:
			size = 100<<10 + rnd.Intn(1200<<10)
		}
		op := sl.OpBinary
		if rnd.Intn(2) == 0 {
			op = sl.OpText
		}
		var p []byte
		if op == sl.OpText {
			head := fmt.Sprintf(`{"seq":%d,"pad":"`, i)
			p = append(make([]byte, 0, len(head)+size+2), head...)
			for j := 0; j < size; j++ {
				p = append(p, byte('a'+rnd.Intn(26)))
			}
			p = append(p, '"', '}')
		} else {
			p = make([]byte, size)
			rnd.Read(p)
			if size >= 4 {
				p[0], p[1], p[2], p[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
			}
		}
		out[i] = testFrame{op: op, p: p}
	}
	return out
}

// checkInterleaved sends n frames each way at once and asserts each side
// reads exactly the other's sequence (ops, bytes and order).
func checkInterleaved(t *testing.T, a, b sl.FrameConn, n int, seed int64) string {
	t.Helper()
	fa, fb := genFrames(seed, n), genFrames(seed+1, n)
	var total, texts int
	for i := range fa {
		total += len(fa[i].p) + len(fb[i].p)
		if fa[i].op == sl.OpText {
			texts++
		}
		if fb[i].op == sl.OpText {
			texts++
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	send := func(c sl.FrameConn, fs []testFrame) {
		defer wg.Done()
		for i, f := range fs {
			if err := c.WriteFrame(f.op, f.p); err != nil {
				errs <- fmt.Errorf("write %d: %v", i, err)
				return
			}
		}
	}
	recv := func(name string, c sl.FrameConn, fs []testFrame) {
		defer wg.Done()
		for i, f := range fs {
			op, p, err := c.ReadFrame()
			if err != nil {
				errs <- fmt.Errorf("%s: read %d: %v", name, i, err)
				return
			}
			if op != f.op || !bytes.Equal(p, f.p) {
				errs <- fmt.Errorf("%s: frame %d: got op 0x%x %d bytes, want op 0x%x %d bytes (out of order or corrupted)", name, i, op, len(p), f.op, len(f.p))
				return
			}
		}
	}
	t0 := time.Now()
	wg.Add(4)
	go send(a, fa)
	go send(b, fb)
	go recv("operator<-host", b, fa)
	go recv("host<-operator", a, fb)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(5 * time.Minute):
		t.Fatal("interleaving test timed out")
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	el := time.Since(t0)
	return fmt.Sprintf("%d frames (%d text) in order, %.1f MiB both ways at once in %v (%.0f Mbit/s aggregate)",
		2*n, texts, float64(total)/(1<<20), el.Round(time.Millisecond), float64(total)*8/el.Seconds()/1e6)
}

func runOrder(t *testing.T, ice sl.ICEOptions, want sl.Path) {
	e := newEnv(t, nil)
	_, hs, os, _, _ := pair(t, e, ice, ice)
	if hs.Path() != want || os.Path() != want {
		t.Fatalf("path host=%s operator=%s, want %s", hs.Path(), os.Path(), want)
	}
	t.Logf("%s: %s", want, checkInterleaved(t, hs, os, 600, 11))
}

// TestOrderDirect / TestOrderRelayUDP / TestOrderRelayTCP: text and binary
// frames interleaved at random, from both sides at once, arrive in the exact
// order they were sent, on every path.
func TestOrderDirect(t *testing.T) { runOrder(t, loopback, sl.PathDirect) }

func TestOrderRelayUDP(t *testing.T) {
	r := loopback
	r.ForceRelay = true
	runOrder(t, r, sl.PathRelayUDP)
}

func TestOrderRelayTCP(t *testing.T) {
	r := loopback
	r.ForceRelay, r.TURNTCPOnly = true, true
	runOrder(t, r, sl.PathRelayTCP)
}
