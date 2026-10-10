package engine

// Golden non-regression of the viewer protocol: the init messages, the
// feature lists, the binary frame layouts and the control replies are
// recorded once in testdata/ and every later build must produce exactly the
// same bytes. Platform-independent outputs live in protocol_golden.json; the
// feature lists (which depend on the OS and on what this machine supports)
// live in protocol_golden_<goos>_<conditions>.json and are compared only
// where such a file exists.
//
// Regenerate (only when a protocol change is intended):
//
//	go test -run TestProtocolGolden -update-golden .

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the protocol golden files")

func goldenJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "marshal error: " + err.Error()
	}
	return string(b)
}

func goldenMonitors() []MonitorInfo {
	return []MonitorInfo{
		{Index: 0, Name: `\\.\DISPLAY1`, X: 0, Y: 0, Width: 2560, Height: 1440, Primary: true, DPI: 144, Scale: 150},
		{Index: 1, Name: `\\.\DISPLAY2`, X: -1920, Y: 200, Width: 1920, Height: 1080, DPI: 96, Scale: 100},
		{Index: 2, Name: `\\.\DISPLAY3`, X: 2560, Y: -400, Width: 1080, Height: 1920, Rotation: 90},
		{Index: 3, Name: `detached`, X: 0, Y: 0, Width: 0, Height: 0},
	}
}

// goldenPure: outputs that depend on nothing but their inputs.
func goldenPure() map[string]string {
	out := map[string]string{}
	mons := goldenMonitors()

	// Frame type bytes (agent -> viewer and viewer -> agent).
	out["frameTypes"] = fmt.Sprintf("jpeg=%02x h264=%02x vp9=%02x h265=%02x av1=%02x audio=%02x audioV2=%02x fileUp=%02x fileDown=%02x",
		FrameTypeJPEG, FrameTypeH264, FrameTypeVP9, FrameTypeH265, FrameTypeAV1, FrameTypeAudio,
		frameTypeAudioV2, FrameTypeFileUp, frameTypeFileDown)
	out["limits"] = fmt.Sprintf("clip=%d file=%d chunk=%d window=%d", clipMaxText, int64(fileMaxBytes), fileChunkSize, fileWindow)

	// Views.
	single := singleTileView(1, -1920, 200, 1920, 1080, 0, false)
	out["view.single"] = goldenJSON(single)
	mosaic := layoutMosaic(mons, agentClampBudget(0, 0, 0))
	out["view.mosaic.default"] = goldenJSON(mosaic)
	out["view.mosaic.budget"] = goldenJSON(layoutMosaic(mons, agentClampBudget(1920, 1080, 1920*1080)))
	var hits []string
	for _, p := range [][2]int{{0, 0}, {10, 10}, {500, 300}, {mosaic.Width - 1, mosaic.Height - 1}, {mosaic.Width / 2, 5}} {
		dx, dy, ok := mosaic.frameToDesktop(p[0], p[1])
		fx, fy := mosaic.desktopToFrame(dx, dy)
		hits = append(hits, fmt.Sprintf("%d,%d->%d,%d,%v->%d,%d", p[0], p[1], dx, dy, ok, fx, fy))
	}
	out["view.mosaic.mapping"] = strings.Join(hits, ";")

	// Binary frames.
	pcm := []byte{1, 0, 2, 0, 0xff, 0x7f, 0x00, 0x80, 3, 0, 4, 0, 5, 0, 6, 0}
	out["audio.downmix"] = hex.EncodeToString(downmixS16ToMono(pcm, 2))
	out["audio.downmix.mono"] = hex.EncodeToString(downmixS16ToMono(pcm, 1))
	out["audio.v2frame"] = hex.EncodeToString(buildAudioV2Frame(2, 48000, 7, 1234567890123, pcm))
	var snd audioV2Sender
	f1 := snd.frame(pcm, 2, 48000)
	f2 := snd.frame(pcm, 2, 48000)
	out["audio.v2sender"] = hex.EncodeToString(f1) + "|" + hex.EncodeToString(f2) + "|" + fmt.Sprint(snd.frame(pcm[:2], 2, 48000) == nil)
	out["file.frame.up"] = hex.EncodeToString(buildFileFrame(FrameTypeFileUp, 0x01020304, 9, []byte("data")))
	out["file.frame.down"] = hex.EncodeToString(buildFileFrame(frameTypeFileDown, 77, 0, nil))
	typ, tid, seq, data, ok := parseFileFrame(buildFileFrame(frameTypeFileDown, 5, 6, []byte{7}))
	out["file.frame.parse"] = fmt.Sprintf("%02x %d %d %x %v", typ, tid, seq, data, ok)

	// JPEG of a fixed gradient (pure Go encoder, quality 15 as on the wire).
	const jw, jh = 32, 16
	bgra := make([]byte, jw*jh*4)
	for i := 0; i < jw*jh; i++ {
		bgra[i*4], bgra[i*4+1], bgra[i*4+2], bgra[i*4+3] = byte(i), byte(i*3), byte(255-i), 255
	}
	if j, err := encodeJPEG(bgra, jw, jh, 15); err == nil {
		out["jpeg"] = hex.EncodeToString(j)
		if _, err := jpeg.Decode(strings.NewReader(string(j))); err != nil {
			out["jpeg.decode"] = err.Error()
		}
	} else {
		out["jpeg"] = "error: " + err.Error()
	}

	// Adaptive bitrate decisions.
	ab := NewAdaptiveBitrate(30)
	var brs []string
	for i := 0; i < 120; i++ {
		d := 5 * time.Millisecond
		if i >= 30 && i < 75 {
			d = 80 * time.Millisecond
		}
		if br := ab.Report(d); br != 0 {
			brs = append(brs, fmt.Sprintf("%d:%d", i, br))
		}
	}
	out["bitrate"] = strings.Join(brs, ",")

	// Key mapping.
	var codes []string
	for _, c := range []string{"KeyA", "KeyZ", "Digit0", "Digit9", "Space", "Enter", "Backspace", "Tab", "Escape",
		"Delete", "Insert", "Home", "End", "PageUp", "PageDown", "ArrowLeft", "ArrowUp", "ArrowRight", "ArrowDown",
		"ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight", "AltLeft", "AltRight", "MetaLeft", "MetaRight",
		"F1", "F12", "Minus", "Equal", "BracketLeft", "BracketRight", "Backslash", "Semicolon", "Quote", "Comma",
		"Period", "Slash", "Backquote", "CapsLock", "NumLock", "ScrollLock", "PrintScreen", "ContextMenu", "Pause",
		"NumpadDecimal", "Unknown"} {
		codes = append(codes, fmt.Sprintf("%s=%d", c, codeToVK(c)))
	}
	out["codeToVK"] = strings.Join(codes, ",")

	// Control replies and detections.
	out["keepalive"] = fmt.Sprint(IsKeepaliveMsg([]byte(`{"type":"keepalive"}`)), IsKeepaliveMsg([]byte(`{"type":"mouse","keepalive":1}`)), IsKeepaliveMsg([]byte(`{"type":"keepalive_x"}`)))
	out["fileRefusal.begin"] = string(FileRefusal([]byte(`{"type":"file_begin","tid":12,"name":"a.txt"}`)))
	out["fileRefusal.get"] = string(FileRefusal([]byte(`{"type":"file_get","tid":3}`)))
	out["fileRefusal.end"] = string(FileRefusal([]byte(`{"type":"file_end","tid":3}`)))
	out["fileDetect"] = fmt.Sprint(IsViewerFileMessage([]byte(`{"type":"file_begin"}`)), IsViewerFileMessage([]byte(`{"type":"clipboard_get"}`)),
		IsAgentFileMessage([]byte(`{"type":"clipboard_files","files":[]}`)), IsAgentFileMessage([]byte(`{"type":"file_error","tid":1}`)))
	out["withoutFileFeatures"] = strings.Join(withoutFileFeatures([]string{featViewGen, featClipboardText, featClipboardFiles, featFileUpload, featFileDownload, featKeepalive}), ",")
	out["blankStatus"] = string(BlankScreenStatusJSON(false, "session_changed")) + "|" + string(BlankScreenStatusJSON(true, ""))
	out["blankOperator"] = string(WithBlankOperator([]byte(`{"type":"set_blank_screen","on":true,"operator":"x"}`), "Op"))
	out["captureStatus"] = goldenJSON(captureStatusMsg("no_frames", "rdp_minimized", "dxgi", true))
	out["encoderStartPlan"] = encoderStartPlan(true, false) + "," + encoderStartPlan(false, true) + "," + encoderStartPlan(false, false)
	out["codecToReapply"] = codecToReapply("") + "," + codecToReapply("h264") + "," + codecToReapply("vp9") + "," + codecToReapply("jpeg")
	out["typeTextResult"] = string(TypeTextResult("id1", false, "session_changed")) + "|" + PeekTypeTextID([]byte(`{"type":"type_text","id":"abc","text":"x"}`))
	out["displayRefusal"] = string(DisplayRefusal([]byte(`{"type":"display_mode_set","id":"r1"}`), "session_changed"))
	out["errCaptureResized"] = errCaptureResized.Error()
	return out
}

// goldenPlatform: the feature lists and init messages, which read what this
// build and machine support.
func goldenPlatform() (map[string]string, string) {
	out := map[string]string{}
	ok, reason := blankScreenAvailable()
	cond := fmt.Sprintf("ib%v_tt%v_dm%v_bs%v_%s_mk%v_h265%v",
		inputBlockSupported(), typeTextSupported(), displayModesSupported(), ok, reason, macKeyMapSupported(), h265Available())
	mons := goldenMonitors()
	view := singleTileView(0, 0, 0, 2560, 1440, 0, true)
	sess := &reachSessionInfo{ID: 2, State: "live", User: "user"}

	savedNoFiles, savedFollow := HelperNoFiles, RunningUnderFollow
	defer func() { HelperNoFiles, RunningUnderFollow = savedNoFiles, savedFollow }()

	for _, c := range []struct {
		name            string
		noFiles, follow bool
	}{{"default", false, false}, {"nofiles", true, false}, {"follow", false, true}} {
		HelperNoFiles, RunningUnderFollow = c.noFiles, c.follow
		out["agentFeatures."+c.name] = strings.Join(agentFeatures(), ",")
		out["init."+c.name] = goldenJSON(buildInitMessage(2560, 1440, 30, streamCodecs(), mons, view, 42, true, 48000, sess))
	}
	HelperNoFiles, RunningUnderFollow = false, false
	mview := layoutMosaic(mons, agentClampBudget(0, 0, 0))
	out["init.mosaic"] = goldenJSON(buildInitMessage(mview.Width, mview.Height, 30, streamCodecs(), mons, mview, 43, false, 0, nil))
	out["directFeatures"] = strings.Join(directStreamFeatures(), ",")
	out["directInit"] = goldenJSON(buildDirectInitMessage(1920, 1080, 30, []string{"h264", "jpeg", "vp9", "av1"}, mons))
	out["streamCodecs"] = strings.Join(streamCodecs(), ",")
	out["blankInitInfo"] = goldenJSON(blankScreenInitInfo())
	out["directBlankReason"] = directBlankReason()
	out["directBlankStatus"] = string(BlankScreenStatusJSON(false, directBlankReason()))
	return out, runtime.GOOS + "_" + cond
}

func compareGolden(t *testing.T, path string, got map[string]string, mustExist bool) {
	t.Helper()
	if *updateGolden {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if mustExist {
			t.Fatalf("golden %s: %v", path, err)
		}
		t.Skipf("no golden for this platform/conditions (%s)", filepath.Base(path))
	}
	var want map[string]string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range want {
		keys = append(keys, k)
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if got[k] != want[k] {
			t.Errorf("%s:\n got  %s\n want %s", k, got[k], want[k])
		}
	}
}

func TestProtocolGolden(t *testing.T) {
	compareGolden(t, filepath.Join("testdata", "protocol_golden.json"), goldenPure(), true)
}

func TestProtocolGoldenPlatform(t *testing.T) {
	got, key := goldenPlatform()
	compareGolden(t, filepath.Join("testdata", "protocol_golden_"+key+".json"), got, false)
}
