package engine

// Link: the message channel of the capture loop (capture_loop_windows.go).
// The capture helper speaks it over a TCP pipe to the service (framing in
// remote_session_windows.go: [u32 LE length][type][payload]); an in-process
// host speaks it directly. The types below are the message kinds, both
// directions; they never travel to the viewer as such: the service relay
// turns them into viewer frames (forwardHelperFrame).

// Link carries typed messages between the capture loop and its transport.
// Send must be safe for concurrent use (the loop sends from several
// goroutines); Recv is called from one goroutine and returns an error once
// the link is gone.
type Link interface {
	Send(msgType byte, payload []byte) error
	Recv() (msgType byte, payload []byte, err error)
}

// Message types (pipeType*): helper = the capture loop, service = its peer.
const (
	PipeTypeInit      = byte(0x01) // helper → service: init JSON
	PipeTypeFrame     = byte(0x02) // helper → service: H.264 NAL units
	PipeTypeInput     = byte(0x03) // service → helper: input JSON
	PipeTypeStop      = byte(0x04) // service → helper: stop signal
	PipeTypeJPEGFrame = byte(0x05) // helper → service: JPEG frame data
	PipeTypeVP9Frame  = byte(0x06) // helper → service: VP9 frame data
	PipeTypeControl   = byte(0x07) // helper → service: JSON control (forwarded as WS text)
	PipeTypeH265Frame = byte(0x08) // helper → service: H.265 frame data
	PipeTypeAV1Frame  = byte(0x09) // helper → service: AV1 frame data
	PipeTypeAudioData = byte(0x0A) // helper → service: audio PCM data
	PipeTypeSAS       = byte(0x0B) // helper → service: request SendSAS (Ctrl+Alt+Del). Must fire from Session 0.
	PipeTypeFileData  = byte(0x0C) // service → helper: viewer file-upload data (a 0x20 frame)
	PipeTypeFileDown  = byte(0x0D) // helper → service: file-download data (a 0x21 frame) → WS binary
	PipeTypeAudioV2   = byte(0x0E) // helper → service: a full 0x07 audio-v2 frame → WS binary
	PipeTypeHello     = byte(0x0F) // helper → service: the one-time nonce, first frame (leg handshake)
)
