//go:build windows

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// helperBlockOwner is the block owner key for the capture loop. One
// helper process serves exactly one viewer, so a single constant owner is
// enough; the set-based manager still guarantees the block is dropped when this
// viewer's stream ends however it ends.
const helperBlockOwner = "helper"

// RunCaptureLoop is the capture side of a remote session on Windows, in the
// session being shown: capture (DXGI / GDI / Magnification, one monitor or the
// mosaic), encoders, input dispatch, clipboard, file transfers, audio, blank
// screen, display modes. It talks to its peer through link (capture helper:
// the service pipe). Returns nil when the stream ends normally (stop message,
// broken link, capture gone), an error when it could not start.
func RunCaptureLoop(link Link) error {
	pipeSendC := link.Send

	// Lock this goroutine to its OS thread for the entire stream lifetime.
	// COM/DXGI/WMF require all calls on the same thread where CoInitializeEx ran.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Raise the helper to ABOVE_NORMAL for the whole stream so capture / encode
	// keeps up under CPU load, restored on every exit (the helper also exits at
	// session end — nothing long-lived keeps a raised priority).
	restorePriority := raiseHelperPriority()
	defer restorePriority()

	// A previous helper of this session may have died with full-window drag
	// forced on (blank screen guard): put the real value back before streaming.
	bgRecoverStaleDragMarker(CurrentSessionID())

	// ── Init capture ─────────────────────────────────────────────────────────
	if err := captureInit(); err != nil {
		return fmt.Errorf("captureInit failed: %w", err)
	}
	defer captureClose()

	w := captureWidth()
	h := captureHeight()
	if w == 0 || h == 0 {
		return fmt.Errorf("invalid capture dimensions %dx%d", w, h)
	}

	fps := 30
	bitrate := 20_000_000

	// ── Init encoder (OpenH264 > WMF > JPEG fallback) ───────────────────────
	useOpenH264 := false
	if openH264Available() {
		if err := openH264Init(w, h, fps, bitrate); err != nil {
			log.Printf("helper: OpenH264 init failed: %v — trying WMF", err)
		} else {
			useOpenH264 = true
			log.Printf("helper: using OpenH264 encoder")
		}
	}
	wmfOK := false
	if !useOpenH264 {
		if _, err := encoderInit(w, h, fps, bitrate); err != nil {
			log.Printf("helper: WMF encoderInit failed: %v — JPEG only", err)
		} else {
			wmfOK = true
		}
	}
	// JPEG when neither H.264 encoder started (encoderStartPlan): the frames
	// used to go through the uninitialised WMF encoder and fail silently.
	startEncoder := encoderStartPlan(useOpenH264, wmfOK)
	log.Printf("helper: encoder at start = %s (%dx%d)", startEncoder, w, h)
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

	// Init audio before sending init message so we can report availability
	audioInit()

	// ── Send init message to service ─────────────────────────────────────────
	// Feature-detected init (features/gen/view/monitor + clipboard/files/audio
	// caps). publishSingleView registers the one-tile view so input maps through
	// it (fixes the mixed-DPI offset), and stamps a gen the viewer echoes.
	view, gen := publishSingleView()
	initMsg := buildInitMessage(w, h, fps, streamCodecs(), enumerateMonitors(),
		view, gen, audioInitDone, audioSampleRate(), currentSessionInfo("live"))
	if d := currentInputDesktop(); d != "" {
		initMsg["inputDesktop"] = d
	}
	if d := domainJoinState(); d != "" { // (the vault: username typed only in a domain)
		initMsg["domain"] = d
	}
	initJSON, _ := json.Marshal(initMsg)
	if err := pipeSendC(PipeTypeInit, initJSON); err != nil {
		return fmt.Errorf("send init failed: %w", err)
	}
	log.Printf("helper: streaming %dx%d@%dfps", w, h, fps)

	// Show a persistent indicator that a remote session is active
	ShowWatermark("Remote session active")
	defer HideWatermark()

	stopCh := make(chan struct{})
	// stopOnce guards stopCh: it is now closed both on an explicit stop message
	// AND when the service pipe breaks, so the two paths can't double-close.
	var stopOnce sync.Once
	closeStop := func() { stopOnce.Do(func() { close(stopCh) }) }
	inputCh := make(chan []byte, 64)
	codecCh := make(chan string, 4)
	viewCh := make(chan viewReq, 4)
	blockCh := make(chan bool, 4) // handled by main capture thread
	blankCh := make(chan blankScreenRequest, 4)
	defer stopClipWatch()

	// injectPaste feeds Ctrl+V through the input dispatch thread (attached to
	// the input desktop) rather than calling SendInput from the reader.
	mkKey := func(action, code string, ctrl bool) []byte {
		b, _ := json.Marshal(map[string]interface{}{"type": "key", "action": action, "code": code, "ctrl": ctrl})
		return b
	}
	pasteSeq := [][]byte{mkKey("down", "ControlLeft", true), mkKey("down", "KeyV", true), mkKey("up", "KeyV", true), mkKey("up", "ControlLeft", false)}
	injectPaste := func() {
		for _, k := range pasteSeq {
			inputCh <- k
		}
	}

	// File transfers: uploads into the session user's Downloads,
	// downloads of files the user copied. Partial files are removed if the
	// stream ends mid-transfer. --no-files (the operator may not transfer
	// files in this session, files_gate.go): no transfer state at all, no
	// copied-files list, every file_* request refused.
	var xfer *fileXfer
	if HelperNoFiles {
		log.Printf("helper: file transfers not permitted in this session (--no-files)")
	} else {
		xfer = newHelperFileXfer(pipeSendC)
		helperClipXfer = xfer
		defer xfer.abort()
	}

	// ── Reader goroutine: input / stop from service ───────────────────────────
	go func() {
		defer close(inputCh)
		for {
			msgType, payload, err := link.Recv()
			if err != nil {
				// The service pipe broke with no stop message (service crash,
				// restart, auto-update, or the socket simply closed): tear the
				// capture loop down so its deferred releaseInputBlock runs and the
				// helper exits — an orphan helper must never keep the local user's
				// input frozen.
				closeStop()
				return
			}
			switch msgType {
			case PipeTypeInput:
				var peek struct {
					Type      string `json:"type"`
					Codec     string `json:"codec"`
					Bitrate   int    `json:"bitrate"`
					Index     int    `json:"index"`
					Block     bool   `json:"block"`
					Recording bool   `json:"recording"`
					Enabled   bool   `json:"enabled"`
					Mode      string `json:"mode"`
					MaxWidth  int    `json:"maxWidth"`
					MaxHeight int    `json:"maxHeight"`
					MaxPixels int    `json:"maxPixels"`
				}
				if json.Unmarshal(payload, &peek) == nil {
					switch peek.Type {
					case "keepalive":
						// The service relay drops keepalives (no inactivity
						// timeout on this path); never let one reach the input
						// dispatch (and its diagnostic log) if it gets here.
						continue
					case "set_codec":
						select {
						case codecCh <- peek.Codec:
						default:
						}
						continue
					case "set_bitrate":
						if peek.Bitrate > 0 {
							openH264SetBitrate(peek.Bitrate)
						}
						continue
					case "set_monitor":
						select {
						case viewCh <- viewReq{mode: "monitor", index: peek.Index}:
						default:
						}
						continue
					case "set_view":
						select {
						case viewCh <- viewReq{mode: peek.Mode, index: peek.Index, maxW: peek.MaxWidth, maxH: peek.MaxHeight, maxPixels: peek.MaxPixels}:
						default:
						}
						continue
					case "set_input_block":
						select {
						case blockCh <- peek.Block:
						default:
						}
						continue
					case "set_blank_screen":
						// The page shown to the local user (blank_screen.go);
						// "operator" comes from the service, never the viewer.
						if req, ok := parseBlankScreenRequest(payload); ok {
							select {
							case blankCh <- req:
							default:
							}
						}
						continue
					case "set_recording":
						SetWatermarkRecording(peek.Recording)
						continue
					case "set_audio", "audio_caps":
						audioNegotiated.Store(true)
						audioStreamEnabled.Store(peek.Enabled)
						continue
					case "display_modes_get", "display_mode_set":
						// Resolution selector (displaymode.go): answered on its
						// own goroutine (a mode change takes a moment), never
						// dispatched as input.
						go handleDisplayModeMsg(payload, func(b []byte) { _ = pipeSendC(PipeTypeControl, b) })
						continue
					case "clipboard_set", "clipboard_get", "clipboard_sync":
						handleHelperClipboard(peek.Type, payload, pipeSendC, injectPaste)
						continue
					case "file_begin", "file_end", "file_cancel", "file_get", "file_progress":
						if xfer == nil {
							if reply := FileRefusal(payload); reply != nil {
								_ = pipeSendC(PipeTypeControl, reply)
							}
							continue
						}
						xfer.onControl(peek.Type, payload)
						continue
					}
					if xfer == nil && strings.HasPrefix(peek.Type, "file_") {
						// A later file_* message: refused too, never injected as input.
						if reply := FileRefusal(payload); reply != nil {
							_ = pipeSendC(PipeTypeControl, reply)
						}
						continue
					}
				}
				// Blocking send — the dedicated dispatch goroutine drains this
				// fast (~1ms per input). Previous non-blocking send silently
				// dropped keystrokes when the main loop was busy with a frame.
				inputCh <- payload
			case PipeTypeFileData:
				// A viewer upload frame (0x20), relayed verbatim by the service
				// (dropped when file transfers are not permitted).
				if xfer != nil {
					xfer.onData(payload)
				}
			case PipeTypeStop:
				closeStop()
				return
			}
		}
	}()

	// ── Audio capture (in the user's session context) ───────────────────────
	// Audio v2 (0x07): fixed conversion + seq/pos so the viewer schedules chunks
	// on a playhead (fixes the crunch). Sent only while a viewer that
	// negotiated it (audio_caps / set_audio) has it on. A viewer that never
	// negotiates gets the legacy 0x06 stream (mono, 50 ms chunks) it always
	// had — with the corrected conversion. The loopback buffer is drained on
	// every tick either way, so turning audio on never plays stale sound.
	defer audioClose()
	if audioInitDone {
		go func() {
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			var snd audioV2Sender
			rate := audioSampleRate()
			legacyChunk := rate * legacyAudioChunkMs / 1000 * 2 // bytes of mono s16
			var legacy []byte
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					pcm, ch := audioCaptureV2()
					if len(pcm) == 0 {
						continue
					}
					if !audioNegotiated.Load() {
						legacy = append(legacy, downmixS16ToMono(pcm, ch)...)
						if len(legacy) >= legacyChunk {
							_ = pipeSendC(PipeTypeAudioData, legacy)
							legacy = nil
						}
						continue
					}
					legacy = nil
					if !audioStreamEnabled.Load() {
						continue
					}
					if frame := snd.frame(pcm, ch, rate); frame != nil {
						_ = pipeSendC(PipeTypeAudioV2, frame)
					}
				}
			}
		}()
	}

	// ── Dedicated input-dispatch goroutine ────────────────────────────────────
	// The main capture loop spends 30-60ms per frame on high-res multi-monitor
	// sessions (e.g. 2552×1348 RDP spanned). With inputs in the same select,
	// operator keystrokes pile up behind a frame in flight — user-observed lag
	// was 3-4s on RDP targets even though each rx→SendInput is <2ms. Running
	// input dispatch on its own thread (LockOSThread so SetThreadDesktop is
	// per-thread stable) removes the contention.
	var screenW, screenH atomic.Int32
	screenW.Store(int32(w))
	screenH.Store(int32(h))
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		for payload := range inputCh {
			// The vault's typed-secret script is typed here (attached to the
			// input desktop) and answered with type_text_result; it is never
			// dispatched as ordinary input and never logged. Everything else
			// is a mouse/key event.
			if IsTypeTextMsg(payload) {
				handleTypeText(payload, func(b []byte) { _ = pipeSendC(PipeTypeControl, b) })
				continue
			}
			dispatchInputJSON(payload, int(screenW.Load()), int(screenH.Load()))
		}
	}()

	// Report the input desktop (login / lock / UAC vs the user's desktop) so the
	// viewer highlights the vault key button where a keylogger in the user's
	// session cannot see the typing.
	go watchInputDesktop(stopCh, func(b []byte) { _ = pipeSendC(PipeTypeControl, b) })

	// ── Capture/encode/send loop ──────────────────────────────────────────────
	bgraBuf := make([]byte, w*h*4)
	frameTicker := time.NewTicker(time.Second / time.Duration(fps))
	defer frameTicker.Stop()

	var pts int64
	var tsMs int64
	firstFrameLogged := false
	// No-picture watchdog (rdp_capture.go): the first frame after each init
	// must reach the viewer within firstFrameStall, or the helper logs why
	// and tells the viewer (capture_status) instead of leaving it on a black
	// screen — an RDP session draws nothing while its client is minimised.
	var stall frameStallWatch
	stall.reset(time.Now())
	sendCaptureStatus := func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		_ = pipeSendC(PipeTypeControl, b)
	}
	frameSent := func() {
		if stall.frame() {
			log.Printf("helper: picture back (%s) — capture_status ok", capturePath())
			sendCaptureStatus(captureStatusMsg("ok", "", capturePath(), captureSessionDisplay.remote))
		}
	}
	useJPEG := startEncoder == "jpeg"
	wmfFailures := 0 // consecutive WMF encode errors (wmfFailFallback)
	useVP9 := false
	useH265 := false
	useAV1 := false

	// ── View changes (one monitor / mosaic) ─────────────────────────────────
	// closeEncoders stops every encoder: a new view restarts on H.264 at its
	// own size (the viewer follows the frames it receives).
	closeEncoders := func() {
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
	}
	// stickyCodec is the codec the operator picked (set_codec). Every init
	// restarts the encoder on H.264 — the viewers reset their decoder to H.264
	// on each init and expect the picked codec back as a codec_switch — so
	// reapplyCodec switches to it again after each new init (resolution change,
	// monitor switch, mosaic). If it cannot start at the new size the stream
	// stays on H.264, which the viewer already decodes.
	stickyCodec := ""
	reapplyCodec := func() {
		c := codecToReapply(stickyCodec)
		if c == "" {
			return
		}
		ok := false
		switch c {
		case "jpeg":
			ok = true
		case "vp9":
			ok = vp9EncoderInit(w, h, fps, bitrate/1000) == nil
		case "h265":
			ok = h265Available() && h265EncoderInit(w, h, fps, bitrate/1000) == nil
		case "av1":
			ok = av1Available() && av1EncoderInit(w, h, fps, bitrate/1000) == nil
		}
		if !ok {
			log.Printf("helper: %s cannot restart at %dx%d — staying on H.264", c, w, h)
			return
		}
		if useOpenH264 {
			openH264Close()
			useOpenH264 = false
		}
		useJPEG = c == "jpeg"
		useVP9 = c == "vp9"
		useH265 = c == "h265"
		useAV1 = c == "av1"
		switchJSON, _ := json.Marshal(map[string]string{"type": "codec_switch", "codec": c})
		_ = pipeSendC(PipeTypeControl, switchJSON)
		log.Printf("helper: %s applied again after the new init", c)
	}
	// startView: the capture source changed (monitor, mosaic, fallback,
	// display mode) — restart the encoder at the new size, publish the view
	// for the input mapping and send the new init (size, view, gen) to the
	// viewer. The size is the view's: the stream never encodes a frame of
	// another size than its init announced (a capture that moved again in
	// between is caught on the next tick and re-published).
	startView := func(view reachView, gen int) {
		w, h = view.Width, view.Height
		if w <= 0 || h <= 0 {
			w, h = captureWidth(), captureHeight()
		}
		bgraBuf = make([]byte, w*h*4)
		monOffX, monOffY = captureMonitorOffset()
		setInputMonitorOffset(monOffX, monOffY) // also refreshes the virtual-screen cache
		screenW.Store(int32(w))
		screenH.Store(int32(h))
		if openH264Available() {
			if err := openH264Init(w, h, fps, bitrate); err == nil {
				useOpenH264 = true
			}
		}
		if !useOpenH264 {
			useJPEG = true
		}
		initMsg := buildInitMessage(w, h, fps, streamCodecs(), enumerateMonitors(),
			view, gen, audioInitDone, audioSampleRate(), currentSessionInfo("live"))
		if d := currentInputDesktop(); d != "" {
			initMsg["inputDesktop"] = d
		}
		if d := domainJoinState(); d != "" {
			initMsg["domain"] = d
		}
		j, _ := json.Marshal(initMsg)
		_ = pipeSendC(PipeTypeInit, j)
		stall.reset(time.Now()) // a new init: its first picture is awaited again
		reapplyCodec()
	}
	// resizeSingle: the display mode changed under the single-monitor capture
	// (resolution, orientation, layout, or the capture moved between DXGI, GDI
	// and Magnification). Same view, new geometry: new encoder, new init, input
	// mapping — where the stream used to end (smaller) or freeze (larger), so
	// the operator can see and click Windows' "Keep changes?" in time. At most
	// one per resizeMinInterval; a throttled one is retried on the next tick
	// (the capture is still stale).
	var resizeThrottle resizeGate
	resizeSingle := func(reason string) {
		if !captureMosaicActive {
			if g := currentSingleGeom(); g.rect.W <= 0 || g.rect.H <= 0 {
				return // still re-initialising: nothing to publish yet
			}
		}
		if !resizeThrottle.allow(time.Now()) {
			return
		}
		oldW, oldH := w, h
		closeEncoders()
		view, gen := publishSingleView()
		startView(view, gen)
		log.Printf("helper: %s — stream %dx%d → %dx%d, new init (gen %d)", reason, oldW, oldH, w, h, gen)
	}
	var watch captureWatchdog
	viewError := func(code, msg string) {
		ve, _ := json.Marshal(map[string]interface{}{"type": "view_error", "code": code, "message": msg})
		_ = pipeSendC(PipeTypeControl, ve)
	}
	// singleView switches to one monitor (idx < 0: the best one, like at
	// start). A monitor that cannot be captured falls back to `fallback`,
	// then to the best one, so the stream never stays without a picture.
	singleView := func(idx, fallback int) bool {
		captureClose() // closes mosaic (if active) + single
		closeEncoders()
		var err error
		if idx >= 0 {
			err = captureInitMonitor(idx)
			if err != nil && fallback >= 0 && fallback != idx {
				log.Printf("helper: monitor %d failed (%v) — back to monitor %d", idx, err, fallback)
				err = captureInitMonitor(fallback)
			}
		}
		if idx < 0 || err != nil {
			if err != nil {
				log.Printf("helper: monitor capture failed (%v) — best monitor", err)
			}
			if err = captureInit(); err != nil {
				log.Printf("helper: no monitor can be captured any more: %v", err)
				return false
			}
		}
		view, gen := publishSingleView()
		startView(view, gen)
		return true
	}
	var lastMosaic viewReq // budget of the running mosaic (re-layout keeps it)
	// enterMosaic duplicates the other monitors while the single capture
	// still runs; the monitor the single capture shows is duplicated only
	// after it released it (DXGI: one duplication per output per process —
	// duplicating it first left the primary tile black). A mosaic that
	// fails before that keeps the current stream; one that fails after it
	// restores the single capture on the monitor shown before.
	// Returns false when nothing can be captured any more (the stream ends).
	enterMosaic := func(req viewReq) bool {
		wasMosaic := captureMosaicActive
		prev := -1
		if !wasMosaic && captureActive && !captureMagActive {
			prev = captureTargetMonitor()
		}
		mview, code, merr := mosaicCaptureInit(req.maxW, req.maxH, req.maxPixels)
		if merr != nil {
			log.Printf("helper: mosaic init failed: %v", merr)
			viewError(code, merr.Error())
			if wasMosaic || !captureActive {
				// The running mosaic was reset, or the single capture was
				// released for it: back to one monitor.
				mosaicCaptureClose()
				return singleView(prev, -1)
			}
			return true // the single-monitor stream goes on
		}
		lastMosaic = req
		captureStopSingle() // free the single-monitor duplication
		closeEncoders()
		gen := nextViewGen()
		setCurrentView(mview, gen)
		startView(mview, gen)
		log.Printf("helper: switched to mosaic %dx%d", w, h)
		return true
	}

	// Release the local-input block no matter how the helper exits — a stop
	// message, a broken/closed service pipe (service crash, restart, auto-
	// update), a capture failure, or shutdown. The capture loop below returns on
	// all of these, so this defer always runs; and if the helper is killed
	// outright, Windows removes the process's hooks anyway. Either way a stuck
	// local keyboard/mouse can never outlive the helper.
	defer releaseInputBlock(helperBlockOwner)
	// The blank screen goes with the stream, whatever ends it (a crash takes
	// the helper's windows with it; a hang: the window thread's watchdog).
	defer releaseBlankScreen()
	blankScreenBeat()
	// Put back the screen resolution this stream changed (unless the operator
	// asked to keep it, or someone changed it after): displaymode.go. Runs on
	// every exit the capture loop has (stop, broken pipe, capture failure).
	defer restoreDisplayModes()
	for {
		select {
		case <-stopCh:
			return nil

		case block := <-blockCh:
			// Freeze the LOCAL user only: the low-level hooks pass our injected
			// input, so the operator keeps control. Report the state actually
			// applied, not the request.
			applied := setInputBlock(helperBlockOwner, block)
			confirm, _ := json.Marshal(map[string]interface{}{"type": "input_block_status", "blocked": applied})
			_ = pipeSendC(PipeTypeControl, confirm)

		case <-inputBlockReleased:
			// The local user's escape hatch lifted the block (Ctrl+Alt+Del,
			// lock, UAC): tell the viewer so its button is not left "on".
			confirm, _ := json.Marshal(map[string]interface{}{"type": "input_block_status", "blocked": inputBlockActive()})
			_ = pipeSendC(PipeTypeControl, confirm)
			// The same gesture gives the local user their screen back.
			if blankActive.Load() {
				releaseBlankScreen()
				_ = pipeSendC(PipeTypeControl, BlankScreenStatusJSON(false, "local_user"))
			}

		case req := <-blankCh:
			// Show / remove the page; report the state actually applied.
			_ = pipeSendC(PipeTypeControl, applyBlankScreen(req))

		case reason := <-blankScreenAutoOff:
			// Removed by the window thread itself (watchdog, lost exclusion,
			// display change that could not be followed).
			_ = pipeSendC(PipeTypeControl, BlankScreenStatusJSON(false, reason))

		case req := <-viewCh:
			if req.mode == "mosaic" {
				if !enterMosaic(req) {
					return nil
				}
				continue
			}
			// Single-monitor view (also used for a monitor switch).
			log.Printf("helper: monitor view → %d", req.index)
			prev := -1
			if !captureMosaicActive && captureActive {
				prev = captureTargetMonitor()
			}
			if !singleView(req.index, prev) {
				return nil
			}
			if captureTargetMonitor() != req.index {
				viewError("monitor_not_found", fmt.Sprintf("monitor %d could not be captured", req.index))
			}

		case newCodec := <-codecCh:
			log.Printf("helper: codec switch requested: %s", newCodec)
			stickyCodec = newCodec
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
					if err := openH264Init(w, h, fps, bitrate); err == nil {
						useOpenH264 = true
					}
				}
				if !useOpenH264 {
					useJPEG = true
				}
			case "h265":
				if h265Available() {
					if err := h265EncoderInit(w, h, fps, bitrate/1000); err == nil {
						useH265 = true
					} else {
						useJPEG = true
					}
				} else {
					useJPEG = true
				}
			case "vp9":
				if err := vp9EncoderInit(w, h, fps, bitrate/1000); err == nil {
					useVP9 = true
				} else {
					useJPEG = true
				}
			case "av1":
				if av1Available() {
					if err := av1EncoderInit(w, h, fps, bitrate/1000); err == nil {
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
			log.Printf("helper: switched to %s", newCodec)
			// Send codec_switch confirmation to service (→ browser)
			switchJSON, _ := json.Marshal(map[string]string{"type": "codec_switch", "codec": newCodec})
			_ = pipeSendC(PipeTypeControl, switchJSON)

		case <-frameTicker.C:
			blankScreenBeat() // the blank screen's watchdog (blank_screen.go)
			if !captureMosaicActive {
				// Display mode changes under the single-monitor capture
				// (capture_resize.go): the watchdog covers GDI / Magnification
				// and the virtual screen; the native thread reports DXGI's
				// re-inits through its geometry sequence.
				switch watch.tick(time.Now()) {
				case watchRestart:
					if !singleView(-1, -1) {
						return nil
					}
					continue
				case watchRepublish:
					resizeSingle("capture source changed")
					continue
				}
				if captureSingleStale() {
					resizeSingle("display geometry changed")
					continue
				}
			}
			// No picture since the last init? (rdp_capture.go: logs why, tells the viewer.)
			checkNoPicture(&stall, time.Now(), sendCaptureStatus)
			fw, fh, err := captureFrame(bgraBuf)
			if err != nil {
				stall.noteError(err)
				switch {
				case errors.Is(err, errMosaicLayout):
					// Display settings changed under the mosaic: lay it out again.
					log.Printf("helper: %v — new mosaic layout", err)
					if !enterMosaic(lastMosaic) {
						return nil
					}
				case errors.Is(err, errMosaicLost):
					// Secure desktop (lock, UAC, Ctrl+Alt+Del): only the
					// single-monitor capture follows it.
					log.Printf("helper: %v — back to one monitor", err)
					viewError("secure_desktop", "the mosaic cannot show the secure desktop (lock screen, UAC, Ctrl+Alt+Del): back to one screen")
					mosaicCaptureClose()
					if !singleView(-1, -1) {
						return nil
					}
				case errors.Is(err, errCaptureResized):
					// A larger frame than the stream's (display mode change).
					resizeSingle("larger frame")
				}
				continue
			}
			if !firstFrameLogged && len(bgraBuf) >= 16 {
				firstFrameLogged = true
				log.Printf("helper: first frame captured %dx%d — pixels[0..3] BGRA: %d %d %d %d | [4..7]: %d %d %d %d",
					fw, fh,
					bgraBuf[0], bgraBuf[1], bgraBuf[2], bgraBuf[3],
					bgraBuf[4], bgraBuf[5], bgraBuf[6], bgraBuf[7])
			}
			if fw != w || fh != h {
				// The frame no longer has the stream's size (display mode
				// change): re-publish the view at the new size. This used to
				// end the helper, and with it the stream.
				resizeSingle(fmt.Sprintf("frame %dx%d", fw, fh))
				continue
			}
			if useJPEG {
				jpegData, err := encodeJPEG(bgraBuf, w, h, 15)
				if err != nil {
					continue
				}
				if err := pipeSendC(PipeTypeJPEGFrame, jpegData); err != nil {
					return nil
				}
			} else if useAV1 {
				av1Data, err := av1EncodeFrame(bgraBuf, w, h)
				if err != nil {
					continue
				}
				if len(av1Data) == 0 {
					continue
				}
				if err := pipeSendC(PipeTypeAV1Frame, av1Data); err != nil {
					return nil
				}
			} else if useH265 {
				h265Data, err := h265EncodeFrame(bgraBuf, w, h)
				if err != nil {
					continue
				}
				if len(h265Data) == 0 {
					continue
				}
				if err := pipeSendC(PipeTypeH265Frame, h265Data); err != nil {
					return nil
				}
			} else if useVP9 {
				vp9Data, err := vp9EncodeFrame(bgraBuf, w, h)
				if err != nil {
					continue
				}
				if len(vp9Data) == 0 {
					continue
				}
				if err := pipeSendC(PipeTypeVP9Frame, vp9Data); err != nil {
					return nil
				}
			} else if useOpenH264 {
				nalUnits, err := openH264EncodeFrame(bgraBuf, w, h, tsMs)
				tsMs += int64(1000 / fps)
				if err != nil {
					log.Printf("helper: OpenH264 error: %v", err)
					continue
				}
				if len(nalUnits) == 0 {
					continue
				}
				if err := pipeSendC(PipeTypeFrame, nalUnits); err != nil {
					return nil
				}
			} else {
				nalUnits, err := encodeFrame(bgraBuf, w, h, pts)
				if err != nil {
					// Errors (not the "0 bytes while buffering" case below)
					// used to be dropped silently forever: no picture at all.
					wmfFailures++
					stall.noteError(err)
					if wmfFailures >= wmfFailFallback {
						log.Printf("helper: WMF H.264 failed %d frames in a row (%v) → JPEG fallback", wmfFailures, err)
						useJPEG = true
						encoderClose()
					}
					continue
				}
				wmfFailures = 0
				pts += int64(time.Second/time.Duration(fps)) / 100
				if len(nalUnits) == 0 {
					if encodeInputCount >= jpegFallbackThreshold && encodeOutputCount == 0 {
						log.Printf("helper: WMF H.264 produced 0 output after %d frames → JPEG fallback", encodeInputCount)
						useJPEG = true
						encoderClose()
					}
					continue
				}
				if err := pipeSendC(PipeTypeFrame, nalUnits); err != nil {
					return nil
				}
			}
			// Every branch above that gets here sent a frame to the viewer.
			frameSent()
		}
	}
}

// checkNoPicture runs the no-picture watchdog on a frame tick: logs why the
// capture gives no picture (session display, capture path, last errors) and
// tells the viewer (capture_status no_frames) — once, then a log line every
// stallLogEvery — and asks a remote session's DXGI capture to fall back to GDI
// once.
func checkNoPicture(stall *frameStallWatch, now time.Time, send func(map[string]interface{})) {
	act := stall.check(now)
	if act == stallNone {
		return
	}
	disp := ownSessionDisplay() // the connect state may have changed since captureInit
	captureSessionDisplay = disp
	path := capturePath()
	reason := stallReason(disp)
	if act == stallLogAgain {
		log.Printf("helper: still NO PICTURE (reason=%s, session %d %s, %s, last error=%q)",
			reason, CurrentSessionID(), disp, captureDiag(), stall.lastError)
		return
	}
	log.Printf("helper: NO PICTURE %s after the init — reason=%s, session %d %s, %s, input desktop=%q, last error=%q. "+
		"An RDP session draws nothing while its RDP client window is minimised or disconnected: no capture path gets a picture until it is shown again.",
		firstFrameStall, reason, CurrentSessionID(), disp, captureDiag(), currentInputDesktop(), stall.lastError)
	send(captureStatusMsg("no_frames", reason, path, disp.remote))
	if stall.wantGDIFallback(disp.remote, path) {
		log.Printf("helper: remote session captured by DXGI without a first frame — switching to GDI")
		captureForceGDI()
	}
}
