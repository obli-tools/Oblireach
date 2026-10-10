package engine

// ObliReach viewer ↔ agent protocol — the AGENT side of the feature-detected
// contract the viewers already speak (TypeScript copy: the Obliance client's
// components/remote/reachProtocol.ts; desktop copy:
// client-app/ui.go reachProtocolJS).
//
// Feature detection, not versioning: `init.features` lists what this build can
// do. A viewer only uses a behaviour when the agent advertises it, and old
// agents (no `features`) keep the legacy behaviour. This file is PURE Go (no
// CGo, no build tag) so the geometry and framing are unit-tested without a
// multi-monitor machine (protocol_test.go).

import (
	"bytes"
	"encoding/json"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ── Limits advertised in init ───────────────────────────────────────────────
const (
	clipMaxText   = 1 << 20            // 1 MiB clipboard text cap
	fileMaxBytes  = 2 * 1024 * 1024 * 1024 // 2 GiB upload/download cap
	fileChunkSize = 256 * 1024         // 256 KiB data frames
	fileWindow    = 8                  // unacked data frames in flight
)

// ViewGenCounter bumps on every published view so the viewer can drop input
// aimed at a superseded layout (view_gen).
var ViewGenCounter int64

func nextViewGen() int { return int(atomic.AddInt64(&ViewGenCounter, 1)) }

// audioStreamEnabled gates the 0x07 audio of a viewer that negotiated it:
// off until the viewer turns it on with audio_caps{enabled} / set_audio.
var audioStreamEnabled atomic.Bool

// audioNegotiated: the viewer sent audio_caps / set_audio. Until then the
// viewer is one that never negotiates (Obliance web viewer and desktop
// client-app before the v2 protocol): it expects the legacy 0x06 stream it
// always received (mono PCM, ~50 ms chunks, muted on the viewer side) — an
// agent that waited for a negotiation left those viewers without any sound.
var audioNegotiated atomic.Bool

// legacyAudioChunkMs is the chunk length of the legacy 0x06 stream (what
// the old viewers were tuned for: each chunk is played on arrival).
const legacyAudioChunkMs = 50

// downmixS16ToMono averages interleaved s16le PCM of `channels` channels into
// mono s16le (the legacy 0x06 format). channels <= 1 returns a copy.
func downmixS16ToMono(pcm []byte, channels int) []byte {
	if channels <= 1 {
		return append([]byte(nil), pcm...)
	}
	frames := len(pcm) / (2 * channels)
	out := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		sum := 0
		for c := 0; c < channels; c++ {
			o := (i*channels + c) * 2
			sum += int(int16(uint16(pcm[o]) | uint16(pcm[o+1])<<8))
		}
		v := int16(sum / channels)
		out[i*2] = byte(uint16(v))
		out[i*2+1] = byte(uint16(v) >> 8)
	}
	return out
}

// audioV2Sender stamps each 0x07 frame with a running seq and sample position
// so the viewer's playhead scheduler can space chunks and spot gaps.
type audioV2Sender struct {
	seq uint32
	pos uint64
}

// frame builds a 0x07 audio frame from interleaved s16le PCM and advances the
// counters. channels is 1 or 2. Returns nil for an empty chunk.
func (a *audioV2Sender) frame(pcm []byte, channels, rate int) []byte {
	if channels < 1 {
		channels = 1
	}
	if len(pcm) < 2*channels {
		return nil
	}
	f := buildAudioV2Frame(channels, uint32(rate), a.seq, a.pos, pcm)
	a.seq++
	a.pos += uint64(len(pcm) / (2 * channels))
	return f
}

// reachSessionInfo is init.session / session_state's who/where.
type reachSessionInfo struct {
	ID      int    `json:"id,omitempty"`
	State   string `json:"state,omitempty"`
	Desktop string `json:"desktop,omitempty"`
	User    string `json:"user,omitempty"`
}

// currentSessionInfo describes the session this capture process runs in (the
// helper's user session, or the service session for the direct path).
func currentSessionInfo(state string) *reachSessionInfo {
	id := CurrentSessionID()
	si := &reachSessionInfo{ID: id, State: state}
	for _, s := range EnumerateSessions() {
		if s.ID == id {
			si.User = s.Username
			break
		}
	}
	return si
}

// buildInitMessage assembles the feature-detected init the viewers expect. The
// capture producer (service direct path or helper) calls it; `gen` and `view`
// come from the same producer that will map the input, so they stay in sync.
func buildInitMessage(width, height, fps int, codecs []string, mons []MonitorInfo,
	view reachView, gen int, audioAvail bool, audioRate int, sess *reachSessionInfo) map[string]interface{} {
	m := map[string]interface{}{
		"type":       "init",
		"width":      width,
		"height":     height,
		"fps":        fps,
		"codec":      "h264",
		"codecs":     codecs,
		"monitors":   mons,
		"features":   agentFeatures(),
		"gen":        gen,
		"view":       view,
		"audioRate":  audioRate,
		"audioAvail": audioAvail,
		"clipboard":  map[string]interface{}{"text": true, "push": true, "files": !HelperNoFiles, "maxText": clipMaxText},
		"files": map[string]interface{}{
			"upload": !HelperNoFiles, "download": !HelperNoFiles,
			"maxFile": int64(fileMaxBytes), "chunk": fileChunkSize, "window": fileWindow,
		},
		"audio": map[string]interface{}{"codecs": []string{"pcm16"}, "maxChannels": 2, "rate": audioRate},
		// Blank screen (blank_screen.go): offered with featBlankScreen; when
		// not, the reason lets the viewers say why their button is disabled.
		"blankScreen": blankScreenInitInfo(),
	}
	if view.Mode == "mosaic" {
		m["monitor"] = -1
	} else if len(view.Tiles) == 1 {
		m["monitor"] = view.Tiles[0].Index
	}
	if sess != nil {
		m["session"] = sess
	}
	return m
}

// ── Injected-modifier tracking ──────────────────────────────────────────────
// The agent releases modifiers a viewer reports as up but that are still down
// in the session ("stuck Ctrl"). It must NOT release a modifier it injected
// itself a moment ago, or a viewer that sends per-key flags breaks its own
// chords (the desktop client-app 1.0.x sends Win+R as {Meta,meta:true} then
// {r,meta:false} → Start menu + "r"). We track what we pressed and hand the
// reconciler a protect mask.
//
// The protection only lasts modProtectWindow after the press: a chord is sent
// in one burst (milliseconds), while the case the reconciliation exists for —
// the viewer lost a key-up (browser tab switch with Ctrl held, clipboard
// prompt, focus change) — is only noticed on the operator's NEXT key, seconds
// later. A permanent protection would keep exactly that lost Ctrl stuck.
const modProtectWindow = 1500 * time.Millisecond

var injectedMods struct {
	mu sync.Mutex
	m  [4]bool      // 0 ctrl, 1 shift, 2 alt, 3 meta
	at [4]time.Time // when each was injected down
}

// modNow is the clock of the protection window (tests override it).
var modNow = time.Now

// modBitForVK returns the modifier group index (0..3) for a Windows VK code, or
// -1 for a non-modifier. Left/right/generic variants all map to one group.
func modBitForVK(vk int) int {
	switch vk {
	case 0x11, 0xA2, 0xA3: // VK_CONTROL / L / R
		return 0
	case 0x10, 0xA0, 0xA1: // VK_SHIFT / L / R
		return 1
	case 0x12, 0xA4, 0xA5: // VK_MENU (Alt) / L / R
		return 2
	case 0x5B, 0x5C: // VK_LWIN / VK_RWIN
		return 3
	}
	return -1
}

// modProtectMask is the bitmask (bit0 ctrl … bit3 meta) of modifiers this
// stream injected down less than modProtectWindow ago and has not released —
// passed to inputReleaseStaleModifiers.
func modProtectMask() int {
	injectedMods.mu.Lock()
	defer injectedMods.mu.Unlock()
	now := modNow()
	mask := 0
	for i, down := range injectedMods.m {
		if down && now.Sub(injectedMods.at[i]) < modProtectWindow {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

func trackInjectedModifier(vk int, down bool) {
	i := modBitForVK(vk)
	if i < 0 {
		return
	}
	injectedMods.mu.Lock()
	if down {
		// Every press (also a key-repeat, or a new press after the reconciler
		// released a lost one) starts the window again.
		injectedMods.at[i] = modNow()
	}
	injectedMods.m[i] = down
	injectedMods.mu.Unlock()
}

func inputResetModifierTracking() {
	injectedMods.mu.Lock()
	injectedMods.m = [4]bool{}
	injectedMods.at = [4]time.Time{}
	injectedMods.mu.Unlock()
}

// ── Current view (input mapping) ────────────────────────────────────────────
// The capture side publishes the active view (single-tile or mosaic) here; the
// input side reads it to map frame pixels → physical desktop pixels through the
// right tile. `gen` bumps on every view change so late input aimed at the old
// layout is dropped (view_gen). Set only on the platform doing the capture
// (Windows today); unset means the legacy offset-based mapping is used.
var curView struct {
	mu   sync.RWMutex
	v    reachView
	gen  int
	set  bool
}

func setCurrentView(v reachView, gen int) {
	curView.mu.Lock()
	curView.v = v
	curView.gen = gen
	curView.set = true
	curView.mu.Unlock()
}

func clearCurrentView() {
	curView.mu.Lock()
	curView.set = false
	curView.mu.Unlock()
}

func getCurrentView() (reachView, int, bool) {
	curView.mu.RLock()
	defer curView.mu.RUnlock()
	return curView.v, curView.gen, curView.set
}

// ── Feature flags (init.features) ───────────────────────────────────────────
const (
	featViewGen       = "view_gen"        // agent stamps init.gen; drops input aimed at an old view
	featMosaic        = "mosaic"          // set_view mode:"mosaic" — all monitors in one frame
	featClipboardText = "clipboard_text"  // clipboard_set / clipboard_get answered with clipboard_content
	featClipboardPush = "clipboard_push"  // clipboard_sync push: agent sends remote changes unprompted
	featClipboardFiles = "clipboard_files" // clipboard_files list of files copied on the remote
	featFileUpload    = "file_upload"     // file_begin/…/0x20 data → the session user's Downloads
	featFileDownload  = "file_download"   // file_get → 0x21 data (a file copied on the remote)
	featAudioV2       = "audio_v2"        // 0x07 audio frames, negotiated by audio_caps/set_audio
	featSessionFollow = "session_follow"  // agent follows session changes, emits session_state
	featErrorCodes    = "error_codes"     // error messages carry a machine code + retry flag
	featKeepalive     = "keepalive"       // viewer {"type":"keepalive"} resets the inactivity timeout (see IsKeepaliveMsg)
	// featBlankScreen ("blank_screen", blank_screen.go): set_blank_screen shows the local user an
	// "Oblireach session in progress" page while the operator keeps seeing the desktop.
	featInputBlockLocal = "input_block_local" // set_input_block freezes only the LOCAL user, not the operator (Windows LL hooks); Oblireach Desktop gates its "cut local input" button on it (the web viewer does not check it yet)
	// featLockOnEnd ("lock_on_end", agent lock_on_end.go): the service locks the
	// session again when the connection ends if it was locked at connect
	// (session_lock_state agent -> viewer, set_lock_on_end viewer -> agent).
	featLockOnEnd = "lock_on_end"
)

// lockOnEndSupported: the end-of-connection lock is done by the Windows
// service's supervisor (WTS lock state + LockWorkStation in the session).
func lockOnEndSupported() bool { return runtime.GOOS == "windows" }

// RunningUnderFollow is set from the helper's --follow flag. Only a helper
// launched by the supervisor advertises featSessionFollow (agentFeatures): the
// feature means "this stream survives a session change", which only the
// supervised path delivers. A stray helper without the flag keeps the historical
// behaviour (the viewers reconnect).
var RunningUnderFollow bool

// agentFeatures lists ONLY what this build actually implements end to end. A
// viewer enables a behaviour purely from this list, so advertising something
// unimplemented degrades the UX (a clipboard panel that does nothing). It is
// sent by the Windows capture helper (the one path that implements all of
// it); the direct capture path sends directStreamFeatures instead.
// Session-follow (the stream staying up across a session change) is delivered by
// the service-side supervisor and advertised only for a helper it launched
// (RunningUnderFollow); a stray helper without --follow keeps the historical
// behaviour (the viewers reconnect).
func agentFeatures() []string {
	f := []string{
		featViewGen, featMosaic,
		featClipboardText, featClipboardPush, featClipboardFiles,
		featFileUpload, featFileDownload,
		featAudioV2, featErrorCodes,
		featKeepalive,
	}
	// Advertise the local-only input block ONLY where the low-level-hook path
	// exists (Windows). This is the path production Windows agents use (service
	// → cross-session helper), so the desktop and web viewers see the capability
	// on every real Windows machine and can offer their "cut local input"
	// button; elsewhere it stays hidden because set_input_block would be a no-op.
	if inputBlockSupported() {
		f = append(f, featInputBlockLocal)
	}
	// The password vault's "type this saved secret" path exists only on the
	// Windows helper (typeTextSupported). A viewer offers the vault key button
	// purely from featTypeText, and highlights it on the secure desktop from
	// featInputDesktop; where the path is absent, both stay hidden and an older
	// viewer simply never asks. type_text arriving at a build that does not
	// advertise it is refused as an unknown message.
	if typeTextSupported() {
		f = append(f, featTypeText, featInputDesktop, featTypeTextSAS)
	}
	// The resolution selector (displaymode.go) exists only on the Windows
	// helper. A viewer shows its resolution control purely from this flag; on
	// an RDP session the list answer says changeable:false, reason "rdp".
	if displayModesSupported() {
		f = append(f, featDisplayModes)
	}
	// The blank screen (blank_screen.go): only where the page can be kept out
	// of the capture (Windows 10 2004+); elsewhere init.blankScreen says why.
	if ok, _ := blankScreenAvailable(); ok {
		f = append(f, featBlankScreen)
	}
	// Advertise session-follow ONLY for a helper the supervisor launched
	// (--follow): the feature means "this stream survives a session change",
	// which only the supervised service path delivers. With it set, the viewers
	// keep their connection across a switch and show the "following the
	// session…" notice instead of reconnecting.
	if RunningUnderFollow {
		f = append(f, featSessionFollow)
		// Lock the session when the connection ends: the same supervisor owns
		// it (it sees the end of the connection, the helper does not).
		if lockOnEndSupported() {
			f = append(f, featLockOnEnd)
		}
	}
	// File transfers not permitted in this session (--no-files, files_gate.go):
	// the viewers then offer no upload, no download and no copied-files list.
	if HelperNoFiles {
		f = withoutFileFeatures(f)
	}
	return f
}

// directStreamFeatures is init.features of the direct capture path (stream.go:
// the agent captures its own session — Windows interactive mode, macOS,
// Linux). That path implements none of the helper's views, clipboard sync,
// file transfer or audio v2, so it advertises only the keepalive — and it is
// the path that owns the inactivity timeout the keepalive exists for.
//
// It intentionally does NOT advertise featInputBlockLocal: this path is only
// reached on Windows when the agent runs interactively in its own session
// (a developer running the exe directly), never in production where the agent
// is a Session-0 service and always spawns the cross-session helper — so the
// capability rides on agentFeatures(). The direct path still honours a
// set_input_block a viewer sends anyway (stream.go), it just doesn't advertise
// the button.
func directStreamFeatures() []string {
	f := []string{featKeepalive}
	// macOS keyboard mapping (keymap_mac.go): only the darwin agent applies
	// a key message's macMap; the viewers offer it only when listed here.
	if macKeyMapSupported() {
		f = append(f, featKeyMapMac)
	}
	return f
}

// buildDirectInitMessage is the init (and re-init after a monitor switch) of
// the direct capture path. Before the keepalive it carried no `features` at
// all; it now lists directStreamFeatures, nothing more.
func buildDirectInitMessage(width, height, fps int, codecs []string, mons []MonitorInfo) map[string]interface{} {
	return map[string]interface{}{
		"type":     "init",
		"width":    width,
		"height":   height,
		"fps":      fps,
		"codec":    "h264",
		"codecs":   codecs,
		"monitors": mons,
		"features": directStreamFeatures(),
		// No blank screen on this path: the viewers say why (macOS / Linux:
		// "Windows only for now").
		"blankScreen": map[string]interface{}{"available": false, "reason": directBlankReason()},
	}
}

// directBlankReason: why the direct capture path has no blank screen —
// "platform" off Windows, "unsupported" for a Windows agent running
// interactively (a developer's direct run: the service path has it).
func directBlankReason() string {
	if ok, reason := blankScreenAvailable(); !ok {
		return reason
	}
	return "unsupported"
}

// ── Viewer keepalive (featKeepalive) ────────────────────────────────────────
// A session that is only watched (View only, or nobody touching anything)
// sends no input, and the stream's inactivity timeout (streamIdleTimeout)
// ended it after 10 minutes. A viewer that sees featKeepalive sends
// {"type":"keepalive"} every 60 s while its user wants the session kept open
// (a per-user viewer setting: always / while visible / never). The agent only
// restarts its inactivity timer: nothing is injected into the session and
// nothing is answered. The timeout itself is unchanged: a viewer that stops
// sending (hidden with "while visible", "never", or an older viewer) is cut
// after it exactly as before.
//
// Who owns the timer: the direct capture path (stream.go run) — the reader
// hands the keepalive to the capture loop, which restarts idleWatch. The
// Windows service + capture helper path (remote_session_windows.go) has no
// inactivity timeout: the service relay drops the keepalive instead of
// piping it to the helper (and the helper ignores one if it ever gets it).
// An older agent never advertises the feature, so it never receives one.
var keepaliveMark = []byte(`keepalive`)

// IsKeepaliveMsg reports whether a viewer JSON text frame is a keepalive. The
// byte pre-check keeps the service relay from parsing every mouse move.
func IsKeepaliveMsg(payload []byte) bool {
	if !bytes.Contains(payload, keepaliveMark) {
		return false
	}
	var peek struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(payload, &peek) == nil && peek.Type == "keepalive"
}

// ── Frame types ─────────────────────────────────────────────────────────────
// Agent → viewer binary (WS opcode 0x2 / pipe payloads):
//   0x01 JPEG 0x02 H264 0x03 VP9 0x04 H265 0x05 AV1  (video)
//   0x06 legacy audio (PCM s16le mono)   0x07 audio v2 (header + PCM)
//   0x21 file-download data
// Viewer → agent binary:
//   0x20 file-upload data
const (
	frameTypeAudioV2  = byte(0x07)
	FrameTypeFileUp   = byte(0x20) // viewer → agent
	frameTypeFileDown = byte(0x21) // agent → viewer
)

// ── Audio v2 framing ────────────────────────────────────────────────────────
// [0x07][ver u8][codec u8][channels u8][rate u32 LE][seq u32 LE][pos u64 LE][PCM s16le interleaved]
// codec 0 = PCM s16le. `pos` is the running sample position (per channel) so the
// viewer's playhead scheduler can detect gaps/overlaps (fixes the crunch).
const audioV2HeaderLen = 20

func buildAudioV2Frame(channels int, rate uint32, seq uint32, pos uint64, pcm []byte) []byte {
	out := make([]byte, audioV2HeaderLen+len(pcm))
	out[0] = frameTypeAudioV2
	out[1] = 1 // version
	out[2] = 0 // codec: PCM s16le
	out[3] = byte(channels)
	putU32LE(out[4:], rate)
	putU32LE(out[8:], seq)
	putU64LE(out[12:], pos)
	copy(out[audioV2HeaderLen:], pcm)
	return out
}

// ── File-transfer data framing (0x20 / 0x21) ────────────────────────────────
// [type u8][tid u32 LE][seq u32 LE][data]
const FileFrameHeaderLen = 9

func parseFileFrame(buf []byte) (typ byte, tid, seq uint32, data []byte, ok bool) {
	if len(buf) < FileFrameHeaderLen {
		return 0, 0, 0, nil, false
	}
	return buf[0], u32LE(buf[1:]), u32LE(buf[5:]), buf[FileFrameHeaderLen:], true
}

func buildFileFrame(typ byte, tid, seq uint32, data []byte) []byte {
	out := make([]byte, FileFrameHeaderLen+len(data))
	out[0] = typ
	putU32LE(out[1:], tid)
	putU32LE(out[5:], seq)
	copy(out[FileFrameHeaderLen:], data)
	return out
}

func u32LE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func putU32LE(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func putU64LE(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * uint(i)))
	}
}

// ── View / tile geometry (single monitor AND mosaic) ────────────────────────
//
// One coordinate model for both view modes: the viewer always sends frame
// pixels (0..frameW, 0..frameH) and the agent maps them back per tile. A
// single-monitor view is a one-tile view whose Frame is (0,0,w,h) and whose
// Desktop is the monitor's PHYSICAL virtual-desktop rectangle. A mosaic view
// has one tile per monitor, laid out at the monitors' real relative positions.
//
// The tile-relative mapping (find the tile under the point, scale inside it) is
// what makes mixed sizes / offsets / negative origins / portrait land on the
// right pixel — and it fixes the mixed-DPI offset: the offset came from the viewer scaling
// with the wrong (stale) dimensions plus a naive per-monitor add. Here every
// point resolves through the actual per-monitor rect.

type reachRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type reachTile struct {
	Index    int       `json:"index"`
	Frame    reachRect `json:"frame"`   // composite-frame pixels
	Desktop  reachRect `json:"desktop"` // physical virtual-desktop pixels
	Rotation int       `json:"rotation,omitempty"`
	Primary  bool      `json:"primary,omitempty"`
}

type reachView struct {
	Mode   string      `json:"mode"` // "monitor" | "mosaic"
	Width  int         `json:"width"`
	Height int         `json:"height"`
	Scale  float64     `json:"scale"`
	Tiles  []reachTile `json:"tiles"`
	// origin bounding box in physical desktop pixels (not serialised; used for
	// the cursor overlay mapping VirtualToFrame).
	originX, originY int
}

// mosaicBudget caps the composite frame so it stays inside the H.264 level a
// hardware WebCodecs decoder accepts. Defaults keep it ≤ 4096 wide and inside
// ~level 5.2 (8.3 MP). The viewer probes its decoder and passes maxWidth/
// maxHeight/maxPixels in set_view; agentClampBudget applies the hard ceiling.
type mosaicBudget struct {
	maxW, maxH, maxPixels int
}

const (
	mosaicMaxW = 4096
	mosaicMaxH = 2304
	// H.264 level 5.2 luma-sample ceiling (36864 macroblocks × 256). The viewer
	// passes its own decoder-probed budget in set_view; agentClampBudget takes
	// the smaller of the two.
	mosaicMaxPixels = 4096 * 2304 // 9_437_184
)

func agentClampBudget(maxW, maxH, maxPixels int) mosaicBudget {
	b := mosaicBudget{maxW: mosaicMaxW, maxH: mosaicMaxH, maxPixels: mosaicMaxPixels}
	if maxW > 0 && maxW < b.maxW {
		b.maxW = maxW
	}
	if maxH > 0 && maxH < b.maxH {
		b.maxH = maxH
	}
	if maxPixels > 0 && maxPixels < b.maxPixels {
		b.maxPixels = maxPixels
	}
	return b
}

func even(v int) int {
	if v < 2 {
		return 2
	}
	return v &^ 1
}

// singleTileView builds the one-tile view for a single-monitor capture. frameW/
// frameH are the captured texture size; desktop is the monitor's physical rect
// (offX/offY = DXGI DesktopCoordinates origin).
func singleTileView(monIndex, offX, offY, frameW, frameH, rotation int, primary bool) reachView {
	return reachView{
		Mode:   "monitor",
		Width:  frameW,
		Height: frameH,
		Scale:  1,
		Tiles: []reachTile{{
			Index:    monIndex,
			Frame:    reachRect{0, 0, frameW, frameH},
			Desktop:  reachRect{offX, offY, frameW, frameH},
			Rotation: rotation,
			Primary:  primary,
		}},
		originX: offX,
		originY: offY,
	}
}

// layoutMosaic composes every monitor into one frame at its real relative
// position under a single uniform scale (exact geometry, no per-tile stretch).
// Ported from a unit-tested reference implementation and adapted to the wire
// types. `mons` rectangles are PHYSICAL desktop pixels (DXGI DesktopCoordinates
// of a per-monitor-DPI-aware process); X/Y may be negative.
func layoutMosaic(all []MonitorInfo, b mosaicBudget) reachView {
	mons := mosaicMonitors(all)
	if len(mons) == 0 {
		return reachView{}
	}
	minX, minY := math.MaxInt32, math.MaxInt32
	maxX, maxY := math.MinInt32, math.MinInt32
	for _, m := range mons {
		minX = min(minX, m.X)
		minY = min(minY, m.Y)
		maxX = max(maxX, m.X+m.Width)
		maxY = max(maxY, m.Y+m.Height)
	}
	bw, bh := maxX-minX, maxY-minY
	if bw <= 0 || bh <= 0 {
		return reachView{}
	}
	s := 1.0
	if b.maxW > 0 {
		s = math.Min(s, float64(b.maxW)/float64(bw))
	}
	if b.maxH > 0 {
		s = math.Min(s, float64(b.maxH)/float64(bh))
	}
	if b.maxPixels > 0 {
		s = math.Min(s, math.Sqrt(float64(b.maxPixels)/float64(bw*bh)))
	}
	v := reachView{
		Mode:    "mosaic",
		Scale:   s,
		Width:   even(int(math.Floor(float64(bw) * s))),
		Height:  even(int(math.Floor(float64(bh) * s))),
		originX: minX,
		originY: minY,
	}
	for _, m := range mons {
		// Edges from scaled edges (not scaled sizes) so neighbours stay
		// adjacent after rounding — no 1-px seams between screens.
		x0 := int(math.Floor(float64(m.X-minX) * s))
		y0 := int(math.Floor(float64(m.Y-minY) * s))
		x1 := min(int(math.Floor(float64(m.X+m.Width-minX)*s)), v.Width)
		y1 := min(int(math.Floor(float64(m.Y+m.Height-minY)*s)), v.Height)
		v.Tiles = append(v.Tiles, reachTile{
			Index:    m.Index,
			Frame:    reachRect{x0, y0, x1 - x0, y1 - y0},
			Desktop:  reachRect{m.X, m.Y, m.Width, m.Height},
			Rotation: m.Rotation,
			Primary:  m.Primary,
		})
	}
	return v
}

// mosaicMonitors keeps the outputs that are part of the desktop: DXGI also
// enumerates detached outputs (DesktopCoordinates 0×0 at 0,0), which would
// widen the bounding box and give zero-size tiles.
func mosaicMonitors(all []MonitorInfo) []MonitorInfo {
	out := make([]MonitorInfo, 0, len(all))
	for _, m := range all {
		if m.Width > 0 && m.Height > 0 {
			out = append(out, m)
		}
	}
	return out
}

// hitTile returns the index into v.Tiles of the tile under frame point (fx,fy),
// or -1 in a black gap between screens.
func (v reachView) hitTile(fx, fy int) int {
	for i, t := range v.Tiles {
		if fx >= t.Frame.X && fx < t.Frame.X+t.Frame.W && fy >= t.Frame.Y && fy < t.Frame.Y+t.Frame.H {
			return i
		}
	}
	return -1
}

func (v reachView) nearestTile(fx, fy int) int {
	best, bestD := 0, math.MaxInt
	for i, t := range v.Tiles {
		dx := max(t.Frame.X-fx, 0, fx-(t.Frame.X+t.Frame.W-1))
		dy := max(t.Frame.Y-fy, 0, fy-(t.Frame.Y+t.Frame.H-1))
		if d := dx*dx + dy*dy; d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// frameToDesktop maps a frame point to physical virtual-desktop pixels. ok is
// false when the point was in a gap (clamped into the nearest tile) — the
// caller drops button-down events there. Tile-relative so a shared edge never
// sends a click to the neighbouring monitor.
func (v reachView) frameToDesktop(fx, fy int) (dx, dy int, ok bool) {
	if len(v.Tiles) == 0 {
		return fx, fy, true
	}
	i := v.hitTile(fx, fy)
	ok = i >= 0
	if !ok {
		i = v.nearestTile(fx, fy)
	}
	t := v.Tiles[i]
	cx := min(max(fx, t.Frame.X), t.Frame.X+t.Frame.W-1)
	cy := min(max(fy, t.Frame.Y), t.Frame.Y+t.Frame.H-1)
	dx = t.Desktop.X + (cx-t.Frame.X)*t.Desktop.W/max(1, t.Frame.W)
	dy = t.Desktop.Y + (cy-t.Frame.Y)*t.Desktop.H/max(1, t.Frame.H)
	return
}

// desktopToFrame maps a physical cursor position into the composite frame (for
// the mosaic cursor overlay). Uniform scale from the bounding box origin.
func (v reachView) desktopToFrame(dx, dy int) (fx, fy int) {
	s := v.Scale
	if s <= 0 {
		s = 1
	}
	fx = int(math.Floor(float64(dx-v.originX) * s))
	fy = int(math.Floor(float64(dy-v.originY) * s))
	return
}
