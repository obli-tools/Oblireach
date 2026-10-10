package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"runtime"
	"sync"
	"time"
	"unicode"
)

// ── Frame type constants ───────────────────────────────────────────────────────

const (
	FrameTypeJPEG  = byte(0x01)
	FrameTypeH264  = byte(0x02)
	FrameTypeVP9   = byte(0x03)
	FrameTypeH265  = byte(0x04)
	FrameTypeAV1   = byte(0x05)
	FrameTypeAudio = byte(0x06)
)

// encodeJPEG converts BGRA pixel data to JPEG bytes (pure Go, no CGo).
func encodeJPEG(bgra []byte, width, height, quality int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	pix := img.Pix
	// BGRA → RGBA: swap B and R in bulk (4 bytes at a time)
	n := width * height * 4
	for i := 0; i < n; i += 4 {
		pix[i+0] = bgra[i+2] // R
		pix[i+1] = bgra[i+1] // G
		pix[i+2] = bgra[i+0] // B
		pix[i+3] = 255       // A
	}
	jpegBuf.Reset()
	if err := jpeg.Encode(&jpegBuf, img, jpegOpts); err != nil {
		return nil, err
	}
	return jpegBuf.Bytes(), nil
}

// Reusable JPEG encoder state (avoids allocation per frame)
var (
	jpegBuf  bytes.Buffer
	jpegOpts = &jpeg.Options{Quality: 15}
)

const jpegFallbackThreshold = 30 // switch after N frames with 0 H.264 output

// ── Adaptive bitrate ─────────────────────────────────────────────────────────

const (
	bitrateMin      = 1_000_000   // 1 Mbps floor
	bitrateMax      = 100_000_000 // 100 Mbps ceiling
	bitrateStart    = 20_000_000  // 20 Mbps initial — start high, drop if needed
	bitrateWindow   = 15          // adjust every 0.5s at 30fps
	bitrateStepUp   = 1.40        // +40% when healthy
	bitrateStepDown = 0.50        // -50% when congested
)

type AdaptiveBitrate struct {
	current     int
	slowFrames  int // frames where send took > budget
	totalFrames int
	budget      time.Duration // max send time per frame
}

func NewAdaptiveBitrate(fps int) *AdaptiveBitrate {
	return &AdaptiveBitrate{
		current: bitrateStart,
		budget:  time.Second / time.Duration(fps),
	}
}

// report records one frame's send duration and returns the new bitrate
// if an adjustment is needed (0 = no change).
func (ab *AdaptiveBitrate) Report(sendTime time.Duration) int {
	ab.totalFrames++
	if sendTime > ab.budget {
		ab.slowFrames++
	}
	if ab.totalFrames < bitrateWindow {
		return 0
	}

	slowRatio := float64(ab.slowFrames) / float64(ab.totalFrames)
	prev := ab.current

	if slowRatio > 0.2 {
		// >20% frames are slow → reduce
		ab.current = int(float64(ab.current) * bitrateStepDown)
	} else if slowRatio < 0.05 {
		// <5% slow → room to grow
		ab.current = int(float64(ab.current) * bitrateStepUp)
	}

	// Clamp
	if ab.current < bitrateMin {
		ab.current = bitrateMin
	}
	if ab.current > bitrateMax {
		ab.current = bitrateMax
	}

	ab.slowFrames = 0
	ab.totalFrames = 0

	if ab.current != prev {
		return ab.current
	}
	return 0
}

// ── Session management ────────────────────────────────────────────────────────

// StreamHooks lets the owner of a StreamSession hear about it without the
// engine knowing the owner: Failed reports a coded failure the viewer must
// also learn through another channel (the agent: tunnel_failed over the
// command WebSocket); Stopped runs once when the session stops (the agent
// drops it from its token registry). Both optional.
type StreamHooks struct {
	Failed  func(code, message string)
	Stopped func()
}

// StreamSession is one direct-path viewer stream: this process captures its
// own session and streams it over a FrameConn.
type StreamSession struct {
	token  string
	ws     FrameConn
	hooks  StreamHooks
	stopCh chan struct{}
	once   sync.Once
}

// NewStreamSession prepares a stream over conn; token only names it in logs
// and in the local-input block bookkeeping. Run starts it.
func NewStreamSession(token string, conn FrameConn, hooks StreamHooks) *StreamSession {
	return &StreamSession{token: token, ws: conn, hooks: hooks, stopCh: make(chan struct{})}
}

// Done is closed when the session stops.
func (s *StreamSession) Done() <-chan struct{} { return s.stopCh }

// Stop ends the session (idempotent): closes the transport and releases any
// modifier the viewer left pressed.
func (s *StreamSession) Stop() {
	s.once.Do(func() {
		close(s.stopCh)
		_ = s.ws.Close()
		// Never leave a modifier pressed in the session when the viewer goes away.
		inputReleaseAllModifiers()
		inputResetModifierTracking()
		if s.hooks.Stopped != nil {
			s.hooks.Stopped()
		}
		log.Printf("Stream %s: stopped", s.token)
	})
}

// reportFailed hands a coded failure to the owner (StreamHooks.Failed).
func (s *StreamSession) reportFailed(code, message string) {
	if s.hooks.Failed != nil {
		s.hooks.Failed(code, message)
	}
}

// Run manages the full streaming lifecycle (blocks until the stream ends):
//  1. Initialize capture and encoder
//  2. Send init frame
//  3. Start capture/encode/send loop
//  4. Handle incoming input frames
func (s *StreamSession) Run() {
	defer s.Stop()
	// Always release this viewer's local-input block when the stream ends
	// (viewer disconnect, inactivity timeout, capture failure, agent shutdown):
	// a stuck local keyboard/mouse must never outlive the session. Keyed by
	// the stream token so concurrent viewers on the direct path are independent.
	defer releaseInputBlock(s.token)

	// Lock this goroutine to its OS thread for the entire stream lifetime.
	// COM/DXGI/WMF require all calls on the same thread where CoInitializeEx ran.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// ── Initialize capture ────────────────────────────────────────────────────
	if err := captureInit(); err != nil {
		log.Printf("Stream %s: captureInit failed: %v", s.token, err)
		// Coded error over the relay AND the command WS (the viewer may still
		// be waiting; the command channel is the reliable path).
		errMsg, _ := json.Marshal(map[string]interface{}{
			"type": "error", "code": "capture_init_failed", "retry": true,
			"message": "Screen capture unavailable: " + err.Error(),
		})
		_ = s.ws.WriteFrame(0x1, errMsg)
		s.reportFailed("capture_init_failed", err.Error())
		return
	}
	defer captureClose()

	width := captureWidth()
	height := captureHeight()
	if width == 0 || height == 0 {
		log.Printf("Stream %s: invalid capture dimensions %dx%d", s.token, width, height)
		s.reportFailed("capture_init_failed", fmt.Sprintf("invalid capture dimensions %dx%d", width, height))
		return
	}

	fps := 30
	bitrate := 20_000_000 // 20 Mbps

	// ── Initialize encoder (OpenH264 > WMF > JPEG fallback) ─────────────────
	useOpenH264 := false
	if openH264Available() {
		if err := openH264Init(width, height, fps, bitrate); err != nil {
			log.Printf("Stream %s: OpenH264 init failed: %v — trying WMF", s.token, err)
		} else {
			useOpenH264 = true
			log.Printf("Stream %s: using OpenH264 encoder", s.token)
		}
	}
	if !useOpenH264 {
		if _, err := encoderInit(width, height, fps, bitrate); err != nil {
			log.Printf("Stream %s: WMF encoderInit failed: %v — JPEG only", s.token, err)
		}
	}
	defer func() {
		if useOpenH264 {
			openH264Close()
		} else {
			encoderClose()
		}
	}()

	// Set monitor offset for input coordinate mapping
	monOffX, monOffY := captureMonitorOffset()
	setInputMonitorOffset(monOffX, monOffY)

	// ── Send init message ─────────────────────────────────────────────────────
	// Build list of available codecs so the viewer only shows what this agent supports
	availCodecs := []string{"h264", "jpeg"}
	if h265Available() {
		availCodecs = append(availCodecs, "h265")
	}
	availCodecs = append(availCodecs, "vp9", "av1") // always compiled in

	// features: only the keepalive (directStreamFeatures) — see protocol.go.
	initMsg := buildDirectInitMessage(width, height, fps, availCodecs, enumerateMonitors())
	initMsg["audioRate"] = audioSampleRate()
	initMsg["audioAvail"] = audioInitDone

	initJSON, _ := json.Marshal(initMsg)
	if err := s.ws.WriteFrame(0x1, initJSON); err != nil {
		log.Printf("Stream %s: send init failed: %v", s.token, err)
		return
	}

	log.Printf("Stream %s: started %dx%d@%dfps", s.token, width, height, fps)
	ShowWatermark("Remote session active")
	defer HideWatermark()

	// ── Keepalive ─────────────────────────────────────────────────────────────
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				if err := s.ws.WriteFrame(0x9, nil); err != nil {
					s.Stop()
					return
				}
			}
		}
	}()

	// ── Audio capture ────────────────────────────────────────────────────────
	audioInit()
	defer audioClose()
	if audioInitDone {
		go func() {
			ticker := time.NewTicker(50 * time.Millisecond) // 20 fps audio chunks (bigger, cleaner)
			defer ticker.Stop()
			for {
				select {
				case <-s.stopCh:
					return
				case <-ticker.C:
					data := audioCapture()
					if len(data) > 0 {
						frame := make([]byte, 1+len(data))
						frame[0] = FrameTypeAudio
						copy(frame[1:], data)
						_ = s.ws.WriteFrame(0x2, frame)
					}
				}
			}
		}()
	}

	// ── Input handler (browser → agent) ──────────────────────────────────────
	inputCh := make(chan []byte, 64)
	codecCh := make(chan string, 4)
	monitorCh := make(chan int, 4)        // monitor switch requests
	blockCh := make(chan bool, 4)         // input block requests (must run on main thread)
	keepaliveCh := make(chan struct{}, 1) // viewer keepalive → idle timer reset (one pending is enough)
	go func() {
		defer s.Stop()
		for {
			opcode, payload, err := s.ws.ReadFrame()
			if err != nil {
				return
			}
			switch opcode {
			case 0x8: // close
				return
			case 0x9: // ping
				_ = s.ws.WriteFrame(0xA, payload) // pong
			case 0x1: // text = JSON control
				var peek struct {
					Type      string `json:"type"`
					Codec     string `json:"codec"`
					Index     int    `json:"index"`
					Block     bool   `json:"block"`
					Recording bool   `json:"recording"`
				}
				if json.Unmarshal(payload, &peek) == nil {
					switch peek.Type {
					case "keepalive":
						// featKeepalive: restarts the inactivity timer only —
						// nothing injected, nothing answered.
						select {
						case keepaliveCh <- struct{}{}:
						default:
						}
						continue
					case "set_recording":
						SetWatermarkRecording(peek.Recording)
						continue
					case "set_codec":
						select {
						case codecCh <- peek.Codec:
						default:
						}
						continue
					case "set_monitor":
						select {
						case monitorCh <- peek.Index:
						default:
						}
						continue
					case "set_input_block":
						select {
						case blockCh <- peek.Block:
						default:
						}
						continue
					case "clipboard_set":
						// Browser → agent: set clipboard content
						var clipMsg struct {
							Text string `json:"text"`
						}
						if json.Unmarshal(payload, &clipMsg) == nil {
							clipboardSet(clipMsg.Text)
						}
						continue
					case "set_blank_screen":
						// The direct capture path never offers the blank
						// screen (blank_screen.go): a viewer that asks anyway
						// is told it is off, nothing is injected.
						_ = s.ws.WriteFrame(0x1, BlankScreenStatusJSON(false, directBlankReason()))
						continue
					case "clipboard_get":
						// Browser requests clipboard content
						text := clipboardGet()
						clipResp, _ := json.Marshal(map[string]string{"type": "clipboard_content", "text": text})
						_ = s.ws.WriteFrame(0x1, clipResp)
						continue
					}
				}
				select {
				case inputCh <- payload:
				default:
				}
			}
		}
	}()

	// ── Capture/encode/send loop ──────────────────────────────────────────────
	bgraSize := width * height * 4
	bgraBuf := make([]byte, bgraSize)
	frameTicker := time.NewTicker(time.Second / time.Duration(fps))
	defer frameTicker.Stop()

	var pts int64
	var tsMs int64
	useJPEG := false
	useVP9 := false
	useH265 := false
	useAV1 := false
	ab := NewAdaptiveBitrate(fps)

	// Inactivity timeout: disconnect after 10 minutes without any input (a
	// viewer keepalive also restarts it — featKeepalive, stream_idle.go).
	idle := newIdleWatch(streamIdleTimeout, streamIdleWarning)
	defer idle.stop()
	resetIdleTimer := idle.reset

	sendAndAdapt := func(frame []byte) error {
		start := time.Now()
		err := s.ws.WriteFrame(0x2, frame)
		if err != nil {
			return err
		}
		if newBr := ab.Report(time.Since(start)); newBr > 0 {
			if useOpenH264 {
				openH264SetBitrate(newBr)
			}
			// Notify browser of bitrate change
			brMsg, _ := json.Marshal(map[string]interface{}{"type": "bitrate", "bitrate": newBr})
			_ = s.ws.WriteFrame(0x1, brMsg)
			log.Printf("Stream %s: adaptive bitrate → %d kbps", s.token, newBr/1000)
		}
		return nil
	}

	for {
		select {
		case <-s.stopCh:
			return

		case <-idle.warning():
			if idle.takeWarning() {
				warnMsg, _ := json.Marshal(map[string]interface{}{
					"type": "inactivity_warning", "seconds": int(streamIdleWarning.Seconds()),
				})
				_ = s.ws.WriteFrame(0x1, warnMsg)
			}

		case <-idle.expired():
			log.Printf("Stream %s: inactivity timeout — disconnecting", s.token)
			timeoutMsg, _ := json.Marshal(map[string]string{"type": "inactivity_timeout"})
			_ = s.ws.WriteFrame(0x1, timeoutMsg)
			return

		case <-keepaliveCh:
			// Viewer keepalive (a watched session with no input): only the
			// inactivity timer restarts.
			resetIdleTimer()

		case block := <-blockCh:
			// Freeze the LOCAL user only, never the operator: the low-level
			// hooks (input_windows.go) run on their own thread and pass our
			// injected input. Report the state ACTUALLY applied (hooks really
			// installed, or false where the platform has no mechanism) — not the
			// request.
			applied := setInputBlock(s.token, block)
			confirm, _ := json.Marshal(map[string]interface{}{"type": "input_block_status", "blocked": applied})
			_ = s.ws.WriteFrame(0x1, confirm)

		case <-inputBlockReleased:
			// The local user's escape hatch lifted the block (Ctrl+Alt+Del,
			// lock, UAC): tell the viewer so its button is not left "on".
			confirm, _ := json.Marshal(map[string]interface{}{"type": "input_block_status", "blocked": inputBlockActive()})
			_ = s.ws.WriteFrame(0x1, confirm)

		case payload := <-inputCh:
			resetIdleTimer()
			s.handleInput(payload, width, height)

		case newIdx := <-monitorCh:
			resetIdleTimer()
			log.Printf("Stream %s: monitor switch to %d", s.token, newIdx)
			// Tear down and reinit capture on new monitor
			captureClose()
			if useOpenH264 {
				openH264Close()
				useOpenH264 = false
			}
			if useVP9 {
				vp9EncoderClose()
				useVP9 = false
			}
			if useH265 {
				h265EncoderClose()
				useH265 = false
			}
			if useAV1 {
				av1EncoderClose()
				useAV1 = false
			}
			encoderClose()
			useJPEG = false

			if err := captureInitMonitor(newIdx); err != nil {
				log.Printf("Stream %s: monitor switch failed: %v", s.token, err)
				continue
			}
			width = captureWidth()
			height = captureHeight()
			bgraBuf = make([]byte, width*height*4)
			monOffX, monOffY = captureMonitorOffset()
			setInputMonitorOffset(monOffX, monOffY)

			// Reinit encoder
			if openH264Available() {
				if err := openH264Init(width, height, fps, bitrate); err == nil {
					useOpenH264 = true
				}
			}
			if !useOpenH264 {
				useJPEG = true
			}

			// Send new init to browser (the viewers read `features` from
			// every init: it must stay on the re-init too).
			reInit := buildDirectInitMessage(width, height, fps, availCodecs, enumerateMonitors())
			reInitJSON, _ := json.Marshal(reInit)
			_ = s.ws.WriteFrame(0x1, reInitJSON)

		case newCodec := <-codecCh:
			resetIdleTimer()
			log.Printf("Stream %s: codec switch requested: %s", s.token, newCodec)
			// Tear down current encoder
			if useOpenH264 {
				openH264Close()
				useOpenH264 = false
			}
			if useVP9 {
				vp9EncoderClose()
				useVP9 = false
			}
			if useH265 {
				h265EncoderClose()
				useH265 = false
			}
			if useAV1 {
				av1EncoderClose()
				useAV1 = false
			}
			if !useJPEG {
				encoderClose()
			}
			useJPEG = false

			switch newCodec {
			case "h264":
				if openH264Available() {
					if err := openH264Init(width, height, fps, bitrate); err == nil {
						useOpenH264 = true
					}
				}
				if !useOpenH264 {
					useJPEG = true
				}
			case "h265":
				if h265Available() {
					if err := h265EncoderInit(width, height, fps, bitrate/1000); err == nil {
						useH265 = true
					} else {
						log.Printf("Stream %s: H.265 init failed: %v", s.token, err)
						useJPEG = true
					}
				} else {
					useJPEG = true
				}
			case "vp9":
				if err := vp9EncoderInit(width, height, fps, bitrate/1000); err == nil {
					useVP9 = true
				} else {
					useJPEG = true
				}
			case "av1":
				if av1Available() {
					if err := av1EncoderInit(width, height, fps, bitrate/1000); err == nil {
						useAV1 = true
					} else {
						useJPEG = true
					}
				} else {
					useJPEG = true
				}
			case "jpeg":
				useJPEG = true
			}
			log.Printf("Stream %s: switched to %s", s.token, newCodec)
			switchMsg, _ := json.Marshal(map[string]string{"type": "codec_switch", "codec": newCodec})
			_ = s.ws.WriteFrame(0x1, switchMsg)

		case <-frameTicker.C:
			w, h, err := captureFrame(bgraBuf)
			if err != nil {
				if errors.Is(err, errCaptureResized) {
					// The display grew (it used to freeze here): same as a
					// smaller frame below, the viewer reconnects at the new size.
					log.Printf("Stream %s: resolution changed (larger frame), reconnecting", s.token)
					return
				}
				continue
			}
			if w != width || h != height {
				log.Printf("Stream %s: resolution changed %dx%d→%dx%d, reconnecting",
					s.token, width, height, w, h)
				return
			}

			if useJPEG {
				jpegData, err := encodeJPEG(bgraBuf, width, height, 15)
				if err != nil {
					continue
				}
				frame := make([]byte, 1+len(jpegData))
				frame[0] = FrameTypeJPEG
				copy(frame[1:], jpegData)
				if err := sendAndAdapt(frame); err != nil {
					return
				}
			} else if useAV1 {
				av1Data, err := av1EncodeFrame(bgraBuf, width, height)
				if err != nil {
					continue
				}
				if len(av1Data) == 0 {
					continue
				}
				frame := make([]byte, 1+len(av1Data))
				frame[0] = FrameTypeAV1
				copy(frame[1:], av1Data)
				if err := sendAndAdapt(frame); err != nil {
					return
				}
			} else if useH265 {
				h265Data, err := h265EncodeFrame(bgraBuf, width, height)
				if err != nil {
					continue
				}
				if len(h265Data) == 0 {
					continue
				}
				frame := make([]byte, 1+len(h265Data))
				frame[0] = FrameTypeH265
				copy(frame[1:], h265Data)
				if err := sendAndAdapt(frame); err != nil {
					return
				}
			} else if useVP9 {
				vp9Data, err := vp9EncodeFrame(bgraBuf, width, height)
				if err != nil {
					continue
				}
				if len(vp9Data) == 0 {
					continue
				}
				frame := make([]byte, 1+len(vp9Data))
				frame[0] = FrameTypeVP9
				copy(frame[1:], vp9Data)
				if err := sendAndAdapt(frame); err != nil {
					return
				}
			} else if useOpenH264 {
				nalUnits, err := openH264EncodeFrame(bgraBuf, width, height, tsMs)
				tsMs += int64(1000 / fps)
				if err != nil {
					continue
				}
				if len(nalUnits) == 0 {
					continue
				}
				frame := make([]byte, 1+len(nalUnits))
				frame[0] = FrameTypeH264
				copy(frame[1:], nalUnits)
				if err := sendAndAdapt(frame); err != nil {
					return
				}
			} else {
				nalUnits, err := encodeFrame(bgraBuf, width, height, pts)
				if err != nil {
					continue
				}
				pts += int64(time.Second/time.Duration(fps)) / 100
				if len(nalUnits) == 0 {
					if encodeInputCount >= jpegFallbackThreshold && encodeOutputCount == 0 {
						useJPEG = true
						encoderClose()
						switchMsg, _ := json.Marshal(map[string]string{"type": "codec_switch", "codec": "jpeg"})
						_ = s.ws.WriteFrame(0x1, switchMsg)
					}
					continue
				}
				frame := make([]byte, 1+len(nalUnits))
				frame[0] = FrameTypeH264
				copy(frame[1:], nalUnits)
				if err := sendAndAdapt(frame); err != nil {
					return
				}
			}
		}
	}
}

// streamCodecs is the codec list advertised in init (viewer codec picker).
// h265 only when compiled with the build tag; vp9/av1 always present.
func streamCodecs() []string {
	c := []string{"h264", "jpeg"}
	if h265Available() {
		c = append(c, "h265")
	}
	return append(c, "vp9", "av1")
}

// viewReq is a set_view request handed from the WS/pipe reader to the capture
// loop (capture (re)init must run on the capture thread).
type viewReq struct {
	mode                  string // "monitor" | "mosaic"
	index                 int    // monitor index (mode "monitor")
	maxW, maxH, maxPixels int    // viewer decoder budget (mode "mosaic")
}

// dispatchInputJSON parses and dispatches a browser input JSON frame.
// Called both by the service process (direct capture) and by the helper
// process (cross-session capture).
func dispatchInputJSON(payload []byte, screenW, screenH int) {
	var msg struct {
		Type   string  `json:"type"`
		Action string  `json:"action"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Button int     `json:"button"`
		Delta  float64 `json:"delta"`
		Code   string  `json:"code"`
		Key    string  `json:"key"`
		Ctrl   bool    `json:"ctrl"`
		Shift  bool    `json:"shift"`
		Alt    bool    `json:"alt"`
		Meta   bool    `json:"meta"`
		// Caps: the operator's Caps Lock state with a key-down (newer viewers;
		// absent = unknown, nothing is aligned).
		Caps *bool `json:"caps"`
		// MacMap: the viewer's macOS keyboard mapping (keymap_mac.go; absent
		// = 0, none). Only the darwin agent reads it.
		MacMap int  `json:"macMap"`
		Width  int  `json:"width"`
		Height int  `json:"height"`
		Gen    *int `json:"gen"`
	}
	// Vault (type_text): a typed-secret frame must never be treated as
	// keyboard input on this path (stream.go has no reply channel), and never
	// be logged. Drop it before json.Unmarshal touches its content and before
	// any diagnostic line is written. The helper handles type_text on its own
	// dispatch branch (remote_session_windows.go).
	if IsTypeTextMsg(payload) {
		WipeBytes(payload)
		return
	}

	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}

	// View generation (view_gen): input aimed at the previous layout (before a
	// monitor/mosaic switch) is dropped so a click never lands on the wrong
	// screen while the view is changing. A release is never dropped — the key
	// or button must always come back up.
	view, viewGen, haveView := getCurrentView()
	if msg.Gen != nil && haveView && *msg.Gen != viewGen && msg.Action != "up" {
		return
	}

	// Diagnostic log of non-mouse input events (no keystroke content).
	// Password shape: even without the key content,
	// one line per keystroke — down/up pairs, the shift flag, millisecond
	// timestamps — leaks a typed password's length, which characters needed
	// Shift (case/symbols), and inter-key timing, all of which meaningfully
	// narrow a brute-force guess. A plain or Shift-only character key is now
	// never logged at all. Only a system-modifier combo (Ctrl/Alt/Meta — the
	// original purpose: tracing shortcut/SAS handling) is logged, and the
	// shift flag is never included. "sas detected" is still logged below.
	if msg.Type != "mouse" && (msg.Ctrl || msg.Alt || msg.Meta) {
		logInputEvent(fmt.Sprintf("rx: type=%q action=%q ctrl=%v alt=%v meta=%v",
			msg.Type, msg.Action, msg.Ctrl, msg.Alt, msg.Meta))
	}

	switch msg.Type {
	case "mouse":
		x := int(msg.X)
		y := int(msg.Y)
		if haveView {
			// Unified path: frame pixels → the right monitor via the active
			// view's tiles (single-tile or mosaic). Physical desktop coords go
			// straight to SendInput (PMv2 space). A press in a black gap of a
			// mosaic reaches no monitor and is dropped (its release is clamped).
			switch msg.Action {
			case "move":
				vx, vy, _ := view.frameToDesktop(x, y)
				inputMoveDesktop(vx, vy)
			case "down":
				vx, vy, inTile := view.frameToDesktop(x, y)
				if !inTile {
					break
				}
				inputButtonDesktop(msg.Button, true, vx, vy)
			case "up":
				vx, vy, _ := view.frameToDesktop(x, y)
				inputButtonDesktop(msg.Button, false, vx, vy)
			case "scroll":
				inputMouseScroll(int(msg.Delta))
			}
			break
		}
		switch msg.Action {
		case "move":
			inputMouseMove(x, y)
		case "down":
			inputMouseButton(msg.Button, true, x, y)
		case "up":
			inputMouseButton(msg.Button, false, x, y)
		case "scroll":
			inputMouseScroll(int(msg.Delta))
		}

	case "key":
		down := msg.Action == "down"
		// A modifier the viewer reports as released must not stay down in
		// the session (a lost key-up used to leave Ctrl pressed: every later
		// letter became a shortcut). The protect mask keeps modifiers THIS
		// stream injected and has not released: a viewer that
		// sends per-key flags — the desktop client-app's sendSystemKeys emits
		// {Meta,meta:true} then {r,meta:false} — must not make us release the
		// Win we just pressed and turn Win+R into "Start menu + type r".
		inputReleaseStaleModifiers(msg.Ctrl, msg.Shift, msg.Alt, msg.Meta, modProtectMask())
		// Detect Ctrl+Alt+Del / Ctrl+Alt+End and translate to SAS (SendSAS).
		// Obliance's Ctrl+Alt+Del button (ObliReachViewer.tsx) sends three
		// keydown events with the physical `code` field set to "Delete" —
		// not the browser-layout `key` field — so we match both. System
		// Keys panels that send via `key` (localised labels) also work.
		if down && msg.Ctrl && msg.Alt &&
			(msg.Key == "Delete" || msg.Key == "Del" || msg.Key == "End" ||
				msg.Code == "Delete" || msg.Code == "Del" || msg.Code == "End" ||
				msg.Code == "NumpadDecimal") {
			logInputEvent("sas detected")
			InputSASHook()
			break
		}
		// macOS (keymap_mac.go): the darwin agent maps the whole key itself
		// (modifiers, layout, Caps Lock); the other agents never set the hook.
		if inputKeyEventHook != nil && inputKeyEventHook(macKeyIn{
			Down: down, Key: msg.Key, Code: msg.Code,
			Ctrl: msg.Ctrl, Shift: msg.Shift, Alt: msg.Alt, Meta: msg.Meta,
			Caps: msg.Caps, Mode: msg.MacMap,
		}) {
			break
		}
		// Caps Lock: the letter's case comes from the operator's key (e.key)
		// and the remote Caps Lock would invert it when the two sides differ
		// (seen: remote on, operator off: everything upper case, and Caps
		// Lock "did nothing" as it toggled on both sides). Aligned before a
		// letter, like RDP syncs its toggle keys.
		if down && msg.Caps != nil && msg.Code != "CapsLock" {
			if r := []rune(msg.Key); len(r) == 1 && unicode.IsLetter(r[0]) {
				alignCapsLock(*msg.Caps)
			}
		}
		// Try layout-aware mapping from e.key first (handles AZERTY, QWERTZ, etc.)
		if len([]rune(msg.Key)) == 1 {
			vk, mods := inputVKFromKey(msg.Key)
			// A character typed with AltGr on the operator's side (Ctrl+Alt
			// reported, a symbol — not a Ctrl+Alt+letter/digit shortcut), or
			// one that needs AltGr on the remote layout: typed in one go with
			// exactly the remote layout's modifiers (inputTapCharFn), never
			// with whatever Ctrl / Alt happen to be held there. Its key-up is
			// then swallowed.
			if vk != 0 && inputTapCharFn != nil {
				r := []rune(msg.Key)[0]
				altgrTyped := msg.Ctrl && msg.Alt && !unicode.IsLetter(r) && !unicode.IsDigit(r)
				needsAltGr := mods&6 == 6
				if altgrTyped || needsAltGr {
					if down {
						inputTapCharFn(vk, mods)
						logInputEvent("altgr char tapped")
					}
					break
				}
			}
			if vk != 0 {
				// If the character needs Shift on the remote layout but the
				// browser didn't set Shift, inject Shift press/release around it.
				needsShift := (mods & 1) != 0
				if down && needsShift && !msg.Shift {
					inputKey(0x10, true) // VK_SHIFT down
				}
				inputKey(vk, down)
				if down && needsShift && !msg.Shift {
					inputKey(0x10, false) // VK_SHIFT up
				}
				break
			}
		}
		// Fallback: use physical code mapping (for F-keys, arrows, modifiers, etc.)
		vk := codeToVK(msg.Code)
		if vk != 0 {
			inputKey(vk, down)
			// Remember modifiers we injected so a later per-key message that
			// reports them "up" does not make us release them.
			trackInjectedModifier(vk, down)
		}

	case "sas":
		// Secure Attention Sequence (Ctrl+Alt+Del)
		InputSASHook()

	case "resize_viewport":
		// No action needed — we capture at native resolution
	}
}

// handleInput is the StreamSession convenience wrapper for dispatchInputJSON.
func (s *StreamSession) handleInput(payload []byte, screenW, screenH int) {
	dispatchInputJSON(payload, screenW, screenH)
}

// InputSASHook is called when the operator presses Ctrl+Alt+Del. Defaults to
// invoking SendSAS locally — correct for the service process (Session 0).
// The helper process overrides this in runHelperMode to pipe the request back
// to the service instead, because SendSAS from Session N with a SYSTEM token
// returns success but produces no secure-desktop transition.
var InputSASHook = InputSAS

// inputLockOnFn reads a lock key's toggle state on the remote session
// (Windows; nil elsewhere).
var inputLockOnFn func(vk int) bool

// capsSyncBroken: the remote Caps Lock state could not be read reliably (a
// press did not change what is read): never touched again by this process,
// so a bad read can never toggle it on every letter.
var capsSyncBroken bool
var capsSyncMu sync.Mutex

// alignCapsLock presses Caps Lock on the remote session when its state
// differs from the operator's, then checks the press took; if it did not,
// it is pressed again (back as it was) and the alignment is given up.
func alignCapsLock(want bool) {
	if inputLockOnFn == nil {
		return
	}
	capsSyncMu.Lock()
	defer capsSyncMu.Unlock()
	if capsSyncBroken {
		return
	}
	on := inputLockOnFn(0x14)
	if on == want {
		return
	}
	inputKey(0x14, true)
	inputKey(0x14, false)
	if inputLockOnFn(0x14) == on {
		inputKey(0x14, true)
		inputKey(0x14, false)
		capsSyncBroken = true
		logInputEvent("caps lock state unreadable: alignment off")
		return
	}
	logInputEvent("caps lock aligned")
}

// inputTapCharFn types one character key with exactly the given modifiers
// (bit0 Shift, bit1 Ctrl, bit2 Alt), whatever is held: set by input_windows.go,
// nil on the other platforms (their keys go the usual way).
var inputTapCharFn func(vk, mods int)

// codeToVK maps a browser KeyboardEvent.code string to a Windows VK_ code.
// Only covers common keys; extend as needed.
func codeToVK(code string) int {
	m := map[string]int{
		"KeyA": 0x41, "KeyB": 0x42, "KeyC": 0x43, "KeyD": 0x44, "KeyE": 0x45,
		"KeyF": 0x46, "KeyG": 0x47, "KeyH": 0x48, "KeyI": 0x49, "KeyJ": 0x4A,
		"KeyK": 0x4B, "KeyL": 0x4C, "KeyM": 0x4D, "KeyN": 0x4E, "KeyO": 0x4F,
		"KeyP": 0x50, "KeyQ": 0x51, "KeyR": 0x52, "KeyS": 0x53, "KeyT": 0x54,
		"KeyU": 0x55, "KeyV": 0x56, "KeyW": 0x57, "KeyX": 0x58, "KeyY": 0x59,
		"KeyZ":   0x5A,
		"Digit0": 0x30, "Digit1": 0x31, "Digit2": 0x32, "Digit3": 0x33, "Digit4": 0x34,
		"Digit5": 0x35, "Digit6": 0x36, "Digit7": 0x37, "Digit8": 0x38, "Digit9": 0x39,
		"Space":        0x20,
		"Enter":        0x0D,
		"Backspace":    0x08,
		"Tab":          0x09,
		"Escape":       0x1B,
		"Delete":       0x2E,
		"Insert":       0x2D,
		"Home":         0x24,
		"End":          0x23,
		"PageUp":       0x21,
		"PageDown":     0x22,
		"ArrowLeft":    0x25,
		"ArrowUp":      0x26,
		"ArrowRight":   0x27,
		"ArrowDown":    0x28,
		"ShiftLeft":    0x10,
		"ShiftRight":   0x10,
		"ControlLeft":  0x11,
		"ControlRight": 0x11,
		"AltLeft":      0x12,
		"AltRight":     0x12,
		"MetaLeft":     0x5B,
		"MetaRight":    0x5C,
		"F1":           0x70, "F2": 0x71, "F3": 0x72, "F4": 0x73,
		"F5": 0x74, "F6": 0x75, "F7": 0x76, "F8": 0x77,
		"F9": 0x78, "F10": 0x79, "F11": 0x7A, "F12": 0x7B,
		"Minus": 0xBD, "Equal": 0xBB,
		"BracketLeft": 0xDB, "BracketRight": 0xDD,
		"Backslash": 0xDC, "Semicolon": 0xBA,
		"Quote": 0xDE, "Comma": 0xBC,
		"Period": 0xBE, "Slash": 0xBF,
		"Backquote":   0xC0,
		"CapsLock":    0x14,
		"NumLock":     0x90,
		"ScrollLock":  0x91,
		"PrintScreen": 0x2C, // VK_SNAPSHOT
		"ContextMenu": 0x5D, // VK_APPS (menu key)
		"Pause":       0x13, // VK_PAUSE
	}
	vk, ok := m[code]
	if !ok {
		return 0
	}
	return vk
}
