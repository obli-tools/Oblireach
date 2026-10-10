package supportlink

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// dataChannel is the subset of a WebRTC data channel the session uses. It is
// implemented over the WebRTC library in peer_webrtc.go, and in memory by the
// framing tests.
type dataChannel interface {
	send([]byte) error
	bufferedAmount() uint64
	setBufferedAmountLowThreshold(uint64)
	onBufferedAmountLow(func())
	onOpen(func())
	onClose(func())
	onMessage(func([]byte))
	close() error
}

// rtcPeer is the subset of a PeerConnection the host and operator flows use.
type rtcPeer interface {
	channel() dataChannel
	createOffer() (string, error)
	acceptOffer(sdp string) (string, error)
	setAnswer(sdp string) error
	addCandidate(c string) error
	onCandidate(func(string))
	onFailed(func(string))
	path() (Path, string)
	close() error
}

// Framing over the data channel. Text and binary frames share ONE ordered,
// reliable data channel, so the peer reads them in exactly the order they
// were written, across both kinds - the WebSocket semantics the engine and
// the viewer rely on (a file_begin JSON before its binary chunks, an init or
// codec_switch JSON before the next keyframe). A frame is split into
// messages of at most chunkSize bytes (16 KiB is the size every WebRTC stack
// accepts), each sent as a binary data channel message starting with one
// header byte: the frame op (OpText or OpBinary) in the low nibble and
// finBit on the last message of the frame. The messages of one frame are
// contiguous on the channel (one writer at a time), so reassembly is a
// simple append. The op travels in a header byte rather than as the data
// channel's string/binary flag because a fragment of a text frame may end in
// the middle of a UTF-8 sequence.
const (
	chunkSize    = 16 * 1024
	finBit       = 0x80
	opMask       = 0x0F
	maxFrameSize = 64 << 20 // engine frames (keyframes, clipboard, file chunks) stay far below

	// Writes block while more than highWater bytes are queued on the
	// channel, and resume when it drains below lowWater - the same
	// back-pressure a TCP socket gives the engine, which drives its frame
	// dropping and adaptive bitrate.
	highWater = 1 << 20
	lowWater  = 256 << 10

	inboxFrames = 64
)

// ErrClosed is returned by WriteFrame after Close.
var ErrClosed = errors.New("supportlink: session closed")

type frame struct {
	op byte
	p  []byte
}

// PeerInfo describes the other side of a session, as known to this side.
type PeerInfo struct {
	// Name is the display name the operator typed (self-declared, never
	// verified). Empty on the operator side.
	Name string
	// Account is the operator identity the rendezvous server authenticated
	// (see ApprovalRequest.Account). Empty on the operator side.
	Account string
}

// Session is an established, end-to-end encrypted link between a host and an
// operator. It implements FrameConn: text and binary frames travel in order
// on one data channel.
type Session struct {
	sid  string
	sas  string
	peer PeerInfo
	rtc  rtcPeer
	// report is the abuse-report token the server gave the host (empty on
	// the operator side, or with a server without reports).
	report string

	w         *chanWriter
	inbox     chan frame
	opened    chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	err       error // set before done is closed
	hookMu    sync.Mutex
	onClosed  func(local bool, err error)

	pathMu     sync.Mutex
	path       Path
	pathDetail string
}

func newSession(sid string, keys sessionKeys, peer PeerInfo, rtc rtcPeer) *Session {
	s := &Session{
		sid: sid, sas: keys.sas, peer: peer, rtc: rtc,
		inbox:  make(chan frame, inboxFrames),
		opened: make(chan struct{}),
		done:   make(chan struct{}),
		path:   PathUnknown,
	}
	s.w = s.attach(rtc.channel())
	rtc.onFailed(func(reason string) { s.closeWith(false, fmt.Errorf("supportlink: %s", reason)) })
	return s
}

// attach wires the channel: open tracking, reassembly into the inbox, and
// the back-pressure signal for the writer.
func (s *Session) attach(dc dataChannel) *chanWriter {
	w := &chanWriter{dc: dc, low: make(chan struct{}, 1)}
	var once sync.Once
	dc.onOpen(func() { once.Do(func() { close(s.opened) }) })
	dc.onClose(func() { s.closeWith(false, io.EOF) })
	dc.setBufferedAmountLowThreshold(lowWater)
	dc.onBufferedAmountLow(func() {
		select {
		case w.low <- struct{}{}:
		default:
		}
	})
	// Messages are delivered one at a time, in order: buf and cur belong to
	// this callback.
	var buf []byte
	var cur byte // op of the frame being reassembled, 0 between frames
	dc.onMessage(func(m []byte) {
		if len(m) == 0 {
			s.closeWith(false, errors.New("supportlink: malformed frame from peer"))
			return
		}
		op := m[0] & opMask
		if (op != OpText && op != OpBinary) || m[0]&^(opMask|finBit) != 0 || (cur != 0 && op != cur) {
			s.closeWith(false, errors.New("supportlink: malformed frame from peer"))
			return
		}
		if len(buf)+len(m)-1 > maxFrameSize {
			s.closeWith(false, errors.New("supportlink: frame from peer too large"))
			return
		}
		cur = op
		buf = append(buf, m[1:]...)
		if m[0]&finBit == 0 {
			return
		}
		f := frame{op: op, p: buf}
		if f.p == nil {
			f.p = []byte{}
		}
		buf, cur = nil, 0
		select {
		case s.inbox <- f: // blocks while the reader is behind: SCTP flow control takes over
		case <-s.done:
		}
	})
	return w
}

// WriteFrame sends one frame. It blocks while the channel is congested.
// Frames are delivered in the order WriteFrame calls return (concurrent
// writers are serialised; a frame is never interleaved with another).
func (s *Session) WriteFrame(op byte, p []byte) error {
	switch op {
	case OpText, OpBinary:
		return s.w.write(op, p, s.done)
	case OpPing, OpPong:
		return nil
	case OpClose:
		return s.Close()
	default:
		return fmt.Errorf("supportlink: unsupported frame op 0x%x", op)
	}
}

// ReadFrame returns the next frame (op 0x1 or 0x2). Frames already received
// are still returned after the peer closed; then the close reason (io.EOF
// for a normal close).
func (s *Session) ReadFrame() (byte, []byte, error) {
	select {
	case f := <-s.inbox:
		return f.op, f.p, nil
	default:
	}
	select {
	case f := <-s.inbox:
		return f.op, f.p, nil
	case <-s.done:
		select {
		case f := <-s.inbox:
			return f.op, f.p, nil
		default:
		}
		return 0, nil, s.err
	}
}

// Close ends the session for both sides.
func (s *Session) Close() error {
	s.closeWith(true, io.EOF)
	return nil
}

func (s *Session) closeWith(local bool, err error) {
	s.closeOnce.Do(func() {
		if err == nil {
			err = io.EOF
		}
		s.err = err
		close(s.done)
		if s.rtc != nil {
			_ = s.rtc.close()
		}
		s.hookMu.Lock()
		f := s.onClosed
		s.hookMu.Unlock()
		if f != nil {
			go f(local, err)
		}
	})
}

// setOnClosed installs the owner's close hook. It runs exactly once, also
// when the session already ended before the hook was installed.
func (s *Session) setOnClosed(f func(local bool, err error)) {
	s.hookMu.Lock()
	s.onClosed = f
	s.hookMu.Unlock()
	select {
	case <-s.done:
		go f(false, s.err)
	default:
	}
}

// SID is the session id assigned by the rendezvous server.
func (s *Session) SID() string { return s.sid }

// SAS is the short verification code ("418 207") both sides display.
func (s *Session) SAS() string { return s.sas }

// Peer describes the other side.
func (s *Session) Peer() PeerInfo { return s.peer }

// ReportToken is the abuse-report token of the session, host side: give it
// to SendReport with SID to report the operator (during the session and for
// a while after it, see the server's report window). Empty on the operator
// side and with a server that does not take reports: then hide the action.
// Keep it in memory only.
func (s *Session) ReportToken() string { return s.report }

// Done is closed when the session ends.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is the reason the session ended (nil while it is open).
func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

// Path is the network path in use (direct or through the relay).
func (s *Session) Path() Path {
	s.refreshPath()
	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	return s.path
}

// PathDetail describes the selected ICE pair ("host/udp <-> relay/udp").
func (s *Session) PathDetail() string {
	s.refreshPath()
	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	return s.pathDetail
}

func (s *Session) refreshPath() {
	select {
	case <-s.done:
		return // keep the last known path
	default:
	}
	p, d := s.rtc.path()
	if p == PathUnknown {
		return
	}
	s.pathMu.Lock()
	s.path, s.pathDetail = p, d
	s.pathMu.Unlock()
}

// waitOpen blocks until the channel is open, the session fails, or the
// timeout expires.
func (s *Session) waitOpen(timeout time.Duration) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-s.opened:
		s.refreshPath()
		return nil
	case <-s.done:
		return s.err
	case <-t.C:
		err := errors.New("supportlink: peer connection timed out")
		s.closeWith(false, err)
		return err
	}
}

var _ FrameConn = (*Session)(nil)

type chanWriter struct {
	dc  dataChannel
	mu  sync.Mutex // the chunks of one frame must stay contiguous
	low chan struct{}
}

func (w *chanWriter) write(op byte, p []byte, done <-chan struct{}) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-done:
		return ErrClosed
	default:
	}
	for {
		n := len(p)
		if n > chunkSize-1 {
			n = chunkSize - 1
		}
		for w.dc.bufferedAmount() > highWater {
			select {
			case <-w.low:
			case <-done:
				return ErrClosed
			case <-time.After(50 * time.Millisecond):
			}
		}
		msg := make([]byte, 1+n)
		msg[0] = op
		if n == len(p) {
			msg[0] |= finBit
		}
		copy(msg[1:], p[:n])
		if err := w.dc.send(msg); err != nil {
			select {
			case <-done:
				return ErrClosed
			default:
				return err
			}
		}
		p = p[n:]
		if len(p) == 0 {
			return nil
		}
	}
}
