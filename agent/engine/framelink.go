package engine

// frameLink: the capture loop's Link (link.go) over a FrameConn, for a host
// that runs the loop in its own process (RunSession on Windows). It plays the
// part of the agent service's relay (remote_session_windows.go
// forwardHelperFrame / relayReader) without the session-follow machinery:
//   - loop → viewer: viewerFrame maps each message to the same viewer frame
//     the relay sends (video and legacy audio prefixed with their frame type,
//     JSON as text), and the adaptive bitrate is measured on the video writes
//     and fed back to the loop as set_bitrate, as the relay does;
//   - viewer → loop: JSON text becomes an input message (keepalives dropped:
//     this path has no inactivity timeout), a 0x20 upload frame becomes file
//     data, a ping is answered, a close ends the link.

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

// errLinkClosed: the link was closed by its owner.
var errLinkClosed = errors.New("engine: link closed")

type linkMsg struct {
	t byte
	p []byte
}

type frameLink struct {
	conn   FrameConn
	in     chan linkMsg
	inject chan []byte // loop commands generated here (set_bitrate)
	done   chan struct{}
	once   sync.Once
	err    error

	abMu sync.Mutex
	ab   *AdaptiveBitrate
}

func newFrameLink(conn FrameConn) *frameLink {
	l := &frameLink{
		conn:   conn,
		in:     make(chan linkMsg),
		inject: make(chan []byte, 4),
		done:   make(chan struct{}),
		ab:     NewAdaptiveBitrate(30),
	}
	go l.reader()
	return l
}

// close ends the link; Recv then returns err (errLinkClosed when nil).
func (l *frameLink) close(err error) {
	l.once.Do(func() {
		if err == nil {
			err = errLinkClosed
		}
		l.err = err
		close(l.done)
	})
}

func (l *frameLink) reader() {
	for {
		op, p, err := l.conn.ReadFrame()
		if err != nil {
			l.close(err)
			return
		}
		switch op {
		case 0x8: // close
			l.close(io.EOF)
			return
		case 0x9: // ping
			_ = l.conn.WriteFrame(0xA, p)
		case 0x1:
			if IsKeepaliveMsg(p) {
				continue
			}
			if !l.push(PipeTypeInput, p) {
				return
			}
		case 0x2:
			if len(p) >= FileFrameHeaderLen && p[0] == FrameTypeFileUp {
				if !l.push(PipeTypeFileData, p) {
					return
				}
			}
		}
	}
}

func (l *frameLink) push(t byte, p []byte) bool {
	select {
	case l.in <- linkMsg{t, p}:
		return true
	case <-l.done:
		return false
	}
}

// Recv implements Link.
func (l *frameLink) Recv() (byte, []byte, error) {
	select {
	case cmd := <-l.inject:
		return PipeTypeInput, cmd, nil
	case m := <-l.in:
		return m.t, m.p, nil
	case <-l.done:
		return 0, nil, l.err
	}
}

// Send implements Link: one loop message to the viewer.
func (l *frameLink) Send(msgType byte, payload []byte) error {
	select {
	case <-l.done:
		return l.err
	default:
	}
	if msgType == PipeTypeSAS {
		// Same as the relay: Ctrl+Alt+Del is raised by the peer (only the
		// loop's InputSASHook sends it; the default calls InputSAS itself).
		InputSAS()
		return nil
	}
	op, frame, video, ok := viewerFrame(msgType, payload)
	if !ok {
		return nil
	}
	start := time.Now()
	if err := l.conn.WriteFrame(op, frame); err != nil {
		return err
	}
	if !video {
		return nil
	}
	l.abMu.Lock()
	newBr := l.ab.Report(time.Since(start))
	l.abMu.Unlock()
	if newBr > 0 {
		cmd, _ := json.Marshal(map[string]interface{}{"type": "set_bitrate", "bitrate": newBr})
		select {
		case l.inject <- cmd:
		default:
		}
		msg, _ := json.Marshal(map[string]interface{}{"type": "bitrate", "bitrate": newBr})
		_ = l.conn.WriteFrame(0x1, msg)
	}
	return nil
}

// viewerFrame maps a capture-loop message to the frame the viewer receives
// (the agent relay's mapping): op 0x1 text / 0x2 binary, the frame bytes,
// whether it is video (adaptive bitrate), and false for messages that never
// reach the viewer (hello, SAS, input-direction types).
func viewerFrame(msgType byte, payload []byte) (op byte, frame []byte, video bool, ok bool) {
	switch msgType {
	case PipeTypeControl, PipeTypeInit:
		return 0x1, payload, false, true
	case PipeTypeAudioV2, PipeTypeFileDown:
		return 0x2, payload, false, true
	case PipeTypeAudioData:
		return 0x2, prefixed(FrameTypeAudio, payload), false, true
	case PipeTypeFrame:
		return 0x2, prefixed(FrameTypeH264, payload), true, true
	case PipeTypeJPEGFrame:
		return 0x2, prefixed(FrameTypeJPEG, payload), true, true
	case PipeTypeVP9Frame:
		return 0x2, prefixed(FrameTypeVP9, payload), true, true
	case PipeTypeH265Frame:
		return 0x2, prefixed(FrameTypeH265, payload), true, true
	case PipeTypeAV1Frame:
		return 0x2, prefixed(FrameTypeAV1, payload), true, true
	}
	return 0, nil, false, false
}

func prefixed(t byte, payload []byte) []byte {
	f := make([]byte, 1+len(payload))
	f[0] = t
	copy(f[1:], payload)
	return f
}
