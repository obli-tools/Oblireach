package supportlink

import (
	"bytes"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// testFrame is one frame of an interleaving scenario.
type testFrame struct {
	op byte
	p  []byte
}

// genFrames draws n frames mixing text and binary, with sizes from empty to
// over 1 MiB, many around the 16 KiB fragmentation boundary and many tiny
// ones. The same seed always gives the same frames. Every frame carries its
// index, so a reordering is reported precisely.
func genFrames(seed int64, n int) []testFrame {
	rnd := rand.New(rand.NewSource(seed))
	out := make([]testFrame, n)
	for i := range out {
		var size int
		switch r := rnd.Intn(100); {
		case r < 45:
			size = rnd.Intn(48) // tiny (header only included)
		case r < 70:
			size = 48 + rnd.Intn(4000)
		case r < 85:
			size = chunkSize - 4 + rnd.Intn(8) // around one chunk
		case r < 95:
			size = rnd.Intn(5 * chunkSize)
		default:
			size = 100<<10 + rnd.Intn(1200<<10) // 100 KiB .. 1.3 MiB
		}
		op := OpBinary
		if rnd.Intn(2) == 0 {
			op = OpText
		}
		var p []byte
		if op == OpText {
			// JSON-like text with its index; padded with ASCII.
			head := fmt.Sprintf(`{"seq":%d,"pad":"`, i)
			p = make([]byte, 0, len(head)+size+2)
			p = append(p, head...)
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

// checkInterleaved sends n generated frames from a to b and n other frames
// from b to a, both directions at once, and asserts that each side reads
// exactly the other's sequence: same ops, same bytes, same order.
func checkInterleaved(t *testing.T, a, b FrameConn, n int, seed int64) string {
	t.Helper()
	fa, fb := genFrames(seed, n), genFrames(seed+1, n)
	var bytesA, bytesB, texts int
	for i := range fa {
		bytesA += len(fa[i].p)
		bytesB += len(fb[i].p)
		if fa[i].op == OpText {
			texts++
		}
		if fb[i].op == OpText {
			texts++
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	send := func(c FrameConn, fs []testFrame) {
		defer wg.Done()
		for i, f := range fs {
			if err := c.WriteFrame(f.op, f.p); err != nil {
				errs <- fmt.Errorf("write %d: %v", i, err)
				return
			}
		}
	}
	recv := func(name string, c FrameConn, fs []testFrame) {
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
	go recv("b<-a", b, fa)
	go recv("a<-b", a, fb)
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
	return fmt.Sprintf("%d frames (%d text) in order, %.1f MiB + %.1f MiB both ways at once in %v (%.0f Mbit/s aggregate)",
		2*n, texts, float64(bytesA)/(1<<20), float64(bytesB)/(1<<20), el.Round(time.Millisecond),
		float64(bytesA+bytesB)*8/el.Seconds()/1e6)
}
