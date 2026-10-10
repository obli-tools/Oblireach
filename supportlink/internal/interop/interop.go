// Package interop is the exchange both build lines run against each other
// in the cross-line tests: the nt6 line (Go 1.20, pion/webrtc v3, built as
// the linkpeer helper) on one side, the modern line (pion/webrtc v4) on the
// other, through a real rendezvous server. Go 1.20 code (no min / max
// builtins, no slices / maps packages).
package interop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/oblireach/supportlink"
)

// FrameConn is the part of *supportlink.Session the exchange uses.
type FrameConn interface {
	WriteFrame(op byte, p []byte) error
	ReadFrame() (byte, []byte, error)
}

// Chat lines of each side: non-ASCII, a bidi override and a control
// character, carried verbatim by the transport (sanitising is the UI's job).
const (
	HostChat     = "Hello from the person, ça va ? 日本語 ‮\x07!"
	OperatorChat = "Bonjour de l'opérateur — привет, مرحبا, 你好"
)

// Seeds of the frame sequences of each side.
const (
	hostSeed     = 1001
	operatorSeed = 2002
)

// Report is what one side measured.
type Report struct {
	ChatRTT  time.Duration // host: chat line out, peer's line in
	RTT      time.Duration // host: median of 20 ping / pong
	Frames   int           // frames received in order
	Texts    int           // of which text frames
	Bytes    int           // payload received
	Elapsed  time.Duration // interleaved phase
	PeerChat string
}

func (r Report) String() string {
	mbps := 0.0
	if r.Elapsed > 0 {
		mbps = float64(r.Bytes) * 8 / r.Elapsed.Seconds() / 1e6
	}
	rtt := ""
	if r.RTT > 0 {
		rtt = fmt.Sprintf(", ctl RTT p50 %v", r.RTT.Round(10*time.Microsecond))
	}
	return fmt.Sprintf("chat ok%s, %d frames (%d text) in order, %.1f MiB in %v (%.0f Mbit/s received while sending)",
		rtt, r.Frames, r.Texts, float64(r.Bytes)/(1<<20), r.Elapsed.Round(time.Millisecond), mbps)
}

type chatMsg struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Run plays one side of the exchange: chat lines both ways, 20 control
// round trips (host pings), then n frames each way at once (text and
// binary, from empty to over 1 MiB, around the 16 KiB fragmentation
// boundary), each side checking it receives the other's exact sequence.
func Run(c FrameConn, host bool, n int) (Report, error) {
	var rep Report
	mine, theirs := OperatorChat, HostChat
	if host {
		mine, theirs = HostChat, OperatorChat
	}
	// 1. Chat (the engine's {"type":"chat"} control message).
	t0 := time.Now()
	b, _ := json.Marshal(chatMsg{Type: "chat", Text: mine})
	if err := c.WriteFrame(supportlink.OpText, b); err != nil {
		return rep, fmt.Errorf("chat send: %w", err)
	}
	op, p, err := c.ReadFrame()
	if err != nil {
		return rep, fmt.Errorf("chat read: %w", err)
	}
	var m chatMsg
	if op != supportlink.OpText || json.Unmarshal(p, &m) != nil || m.Type != "chat" || m.Text != theirs {
		return rep, fmt.Errorf("chat: got op 0x%x %q", op, p)
	}
	rep.ChatRTT, rep.PeerChat = time.Since(t0), m.Text

	// 2. Control round trips.
	var rtts []time.Duration
	for i := 0; i < 20; i++ {
		ping, pong := fmt.Sprintf(`{"type":"ping","seq":%d}`, i), fmt.Sprintf(`{"type":"pong","seq":%d}`, i)
		if host {
			t1 := time.Now()
			if err := c.WriteFrame(supportlink.OpText, []byte(ping)); err != nil {
				return rep, err
			}
			op, p, err := c.ReadFrame()
			if err != nil || op != supportlink.OpText || string(p) != pong {
				return rep, fmt.Errorf("pong %d: op 0x%x %q %v", i, op, p, err)
			}
			rtts = append(rtts, time.Since(t1))
			continue
		}
		op, p, err := c.ReadFrame()
		if err != nil || op != supportlink.OpText || string(p) != ping {
			return rep, fmt.Errorf("ping %d: op 0x%x %q %v", i, op, p, err)
		}
		if err := c.WriteFrame(supportlink.OpText, []byte(pong)); err != nil {
			return rep, err
		}
	}
	if host {
		sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
		rep.RTT = rtts[len(rtts)/2]
	}

	// 3. Interleaved frames, both ways at once.
	ms, ts := operatorSeed, hostSeed
	if host {
		ms, ts = hostSeed, operatorSeed
	}
	out, want := GenFrames(int64(ms), n), GenFrames(int64(ts), n)
	sent := make(chan error, 1)
	t2 := time.Now()
	go func() {
		for i, f := range out {
			if err := c.WriteFrame(f.Op, f.P); err != nil {
				sent <- fmt.Errorf("write %d: %w", i, err)
				return
			}
		}
		sent <- c.WriteFrame(supportlink.OpText, []byte(`{"type":"done"}`))
	}()
	for i, f := range want {
		op, p, err := c.ReadFrame()
		if err != nil {
			return rep, fmt.Errorf("read %d: %w", i, err)
		}
		if op != f.Op || !bytes.Equal(p, f.P) {
			return rep, fmt.Errorf("frame %d: got op 0x%x %d bytes, want op 0x%x %d bytes (out of order or corrupted)", i, op, len(p), f.Op, len(f.P))
		}
		rep.Frames++
		rep.Bytes += len(p)
		if op == supportlink.OpText {
			rep.Texts++
		}
	}
	op, p, err = c.ReadFrame()
	if err != nil || op != supportlink.OpText || string(p) != `{"type":"done"}` {
		return rep, fmt.Errorf("end marker: op 0x%x %q %v", op, p, err)
	}
	rep.Elapsed = time.Since(t2)
	select {
	case err := <-sent:
		if err != nil {
			return rep, err
		}
	case <-time.After(time.Minute):
		return rep, errors.New("sending did not finish")
	}
	return rep, nil
}

// Frame is one generated frame.
type Frame struct {
	Op byte
	P  []byte
}

// fragSize is supportlink's data channel message size.
const fragSize = 16 * 1024

// GenFrames draws n frames mixing text and binary, from empty to over
// 1 MiB, many tiny and many around the fragmentation boundary; every frame
// carries its index. Deterministic for a seed (both sides regenerate the
// other's sequence).
func GenFrames(seed int64, n int) []Frame {
	rnd := rand.New(rand.NewSource(seed))
	out := make([]Frame, n)
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
		op := supportlink.OpBinary
		if rnd.Intn(2) == 0 {
			op = supportlink.OpText
		}
		var p []byte
		if op == supportlink.OpText {
			var sb strings.Builder
			fmt.Fprintf(&sb, `{"seq":%d,"pad":"`, i)
			for j := 0; j < size; j++ {
				sb.WriteByte(byte('a' + rnd.Intn(26)))
			}
			sb.WriteString(`"}`)
			p = []byte(sb.String())
		} else {
			p = make([]byte, size)
			rnd.Read(p)
			if size >= 4 {
				p[0], p[1], p[2], p[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
			}
		}
		out[i] = Frame{Op: op, P: p}
	}
	return out
}
