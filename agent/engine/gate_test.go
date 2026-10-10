package engine

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var allowAll = Options{
	AllowInput: true, AllowClipboard: true, AllowFiles: true,
	AllowBlankScreen: true, AllowInputBlock: true, AllowDisplayModes: true,
}

// gateIn feeds viewer frames through a gate and returns what passed and the
// replies the gate wrote back.
func gateIn(t *testing.T, opts Options, frames ...memFrame) (passed, replies []memFrame) {
	t.Helper()
	c := newMemConn()
	g := newGate(c, opts)
	for _, f := range frames {
		c.send(f.op, f.p)
	}
	c.send(0x1, []byte(`{"type":"end_marker"}`))
	for {
		op, p, err := g.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if op == 0x1 && msgType(p) == "end_marker" {
			break
		}
		passed = append(passed, memFrame{op, p})
	}
	return passed, c.pending()
}

func txt(s string) memFrame { return memFrame{0x1, []byte(s)} }

func types(fs []memFrame) []string {
	var out []string
	for _, f := range fs {
		if f.op == 0x1 {
			out = append(out, msgType(f.p))
		} else {
			out = append(out, "bin")
		}
	}
	return out
}

var viewerMsgs = []memFrame{
	txt(`{"type":"mouse","action":"move","x":1,"y":2}`),
	txt(`{"type":"key","action":"down","code":"KeyA","key":"a"}`),
	txt(`{"type":"sas"}`),
	txt(`{"type":"set_input_block","block":true}`),
	txt(`{"type":"set_blank_screen","on":true}`),
	txt(`{"type":"display_modes_get","id":"d1"}`),
	txt(`{"type":"clipboard_get","id":"c1"}`),
	txt(`{"type":"clipboard_sync","push":true}`),
	txt(`{"type":"file_begin","tid":7,"name":"a.txt","size":3}`),
	{0x2, append([]byte{FrameTypeFileUp}, make([]byte, 12)...)},
	txt(`{"type":"set_codec","codec":"vp9"}`),
	txt(`{"type":"set_view","mode":"mosaic"}`),
	txt(`{"type":"keepalive"}`),
	{0x9, []byte("ping")},
}

func TestGateViewOnlyInbound(t *testing.T) {
	passed, replies := gateIn(t, Options{}, viewerMsgs...)
	if got, want := strings.Join(types(passed), ","), "set_codec,set_view,keepalive,bin"; got != want {
		// the last "bin" is the ping (op 0x9), passed to the session
		t.Fatalf("passed %s, want %s", got, want)
	}
	if passed[3].op != 0x9 {
		t.Fatalf("ping not passed: %v", passed[3])
	}
	want := [][]byte{
		[]byte(`{"blocked":false,"type":"input_block_status"}`),
		BlankScreenStatusJSON(false, "unsupported"),
		DisplayRefusal([]byte(`{"type":"display_modes_get","id":"d1"}`), "unsupported"),
		FileRefusal([]byte(`{"type":"file_begin","tid":7,"name":"a.txt","size":3}`)),
	}
	if len(replies) != len(want) {
		t.Fatalf("replies %v", types(replies))
	}
	for i, w := range want {
		if replies[i].op != 0x1 || !bytes.Equal(replies[i].p, w) {
			t.Errorf("reply %d = %s, want %s", i, replies[i].p, w)
		}
	}
}

func TestGateAllowAllInbound(t *testing.T) {
	passed, replies := gateIn(t, allowAll, viewerMsgs...)
	if len(replies) != 0 {
		t.Fatalf("no reply expected, got %v", types(replies))
	}
	if len(passed) != len(viewerMsgs) {
		t.Fatalf("passed %d of %d: %v", len(passed), len(viewerMsgs), types(passed))
	}
	for i := range passed {
		if passed[i].op != viewerMsgs[i].op || !bytes.Equal(passed[i].p, viewerMsgs[i].p) {
			t.Fatalf("frame %d altered", i)
		}
	}
}

func TestGateInputBlockNeedsInput(t *testing.T) {
	o := Options{AllowInputBlock: true}
	passed, replies := gateIn(t, o, txt(`{"type":"set_input_block","block":true}`))
	if len(passed) != 0 || len(replies) != 1 {
		t.Fatalf("passed %v replies %v", types(passed), types(replies))
	}
}

func TestGateTypeTextNeverPassesAndIsWiped(t *testing.T) {
	secret := []byte(`{"type":"type_text","id":"x","parts":[{"b64":"czNjcmV0"}]}`)
	passed, replies := gateIn(t, allowAll, memFrame{0x1, secret})
	if len(passed) != 0 || len(replies) != 0 {
		t.Fatalf("type_text: passed %v replies %v", types(passed), types(replies))
	}
	if bytes.Contains(secret, []byte("czNjcmV0")) {
		t.Fatal("type_text payload not wiped")
	}
}

func TestGateChat(t *testing.T) {
	var got []string
	o := Options{OnChat: func(s string) { got = append(got, s) }}
	long := strings.Repeat("é", ChatMaxBytes) // 2 bytes per rune
	passed, _ := gateIn(t, o, txt(`{"type":"chat","text":"hello"}`), txt(`{"type":"chat","text":""}`),
		memFrame{0x1, mustJSON(t, map[string]string{"type": "chat", "text": long})})
	if len(passed) != 0 {
		t.Fatalf("chat passed to the session: %v", types(passed))
	}
	if len(got) != 2 || got[0] != "hello" || len(got[1]) != ChatMaxBytes || !strings.HasPrefix(long, got[1]) {
		t.Fatalf("chat lines: %d %q", len(got), got[0])
	}
	// Chat off: dropped, no hook.
	passed, _ = gateIn(t, Options{}, txt(`{"type":"chat","text":"hello"}`))
	if len(passed) != 0 {
		t.Fatal("chat passed with chat off")
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fullInit: an init advertising everything the agent can.
const fullInit = `{"audio":{"codecs":["pcm16"],"maxChannels":2,"rate":48000},"audioAvail":true,"audioRate":48000,` +
	`"blankScreen":{"available":true},"clipboard":{"files":true,"maxText":1048576,"push":true,"text":true},` +
	`"codec":"h264","codecs":["h264","jpeg","vp9","av1"],"domain":"joined",` +
	`"features":["view_gen","mosaic","clipboard_text","clipboard_push","clipboard_files","file_upload","file_download","audio_v2","error_codes","keepalive","input_block_local","type_text","input_desktop","type_text_sas","display_modes","blank_screen","session_follow","key_map_mac"],` +
	`"files":{"chunk":262144,"download":true,"maxFile":2147483648,"upload":true,"window":8},"fps":30,"gen":42,"height":1440,"inputDesktop":"Default","monitor":0,` +
	`"monitors":[],"type":"init","view":{"mode":"mosaic","width":2560,"height":1440,"scale":0.6666666666666666,"tiles":[]},"width":2560}`

func gateOutInit(t *testing.T, o Options) map[string]interface{} {
	t.Helper()
	c := newMemConn()
	g := newGate(c, o)
	if err := g.WriteFrame(0x1, []byte(fullInit)); err != nil {
		t.Fatal(err)
	}
	f := c.next(t, time.Second)
	if bytes.Contains(f.p, []byte("e+")) || !bytes.Contains(f.p, []byte(`"maxFile":2147483648`)) ||
		!bytes.Contains(f.p, []byte(`"scale":0.6666666666666666`)) {
		t.Fatalf("numbers not kept as sent: %s", f.p)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(f.p, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func featureList(m map[string]interface{}) string {
	var out []string
	for _, f := range m["features"].([]interface{}) {
		out = append(out, f.(string))
	}
	return strings.Join(out, ",")
}

func TestGateInitViewOnly(t *testing.T) {
	m := gateOutInit(t, Options{})
	if got, want := featureList(m), "view_gen,mosaic,audio_v2,error_codes,keepalive"; got != want {
		t.Fatalf("features %s, want %s", got, want)
	}
	if c := m["clipboard"].(map[string]interface{}); c["text"] != false || c["push"] != false || c["files"] != false {
		t.Fatalf("clipboard caps %v", c)
	}
	if f := m["files"].(map[string]interface{}); f["upload"] != false || f["download"] != false {
		t.Fatalf("file caps %v", f)
	}
	if b := m["blankScreen"].(map[string]interface{}); b["available"] != false || b["reason"] != "unsupported" {
		t.Fatalf("blank screen %v", b)
	}
	if _, ok := m["inputDesktop"]; ok {
		t.Fatal("inputDesktop kept")
	}
	if _, ok := m["domain"]; ok {
		t.Fatal("domain kept")
	}
}

func TestGateInitAllowAllWithChat(t *testing.T) {
	o := allowAll
	o.OnChat = func(string) {}
	m := gateOutInit(t, o)
	want := "view_gen,mosaic,clipboard_text,clipboard_push,clipboard_files,file_upload,file_download,audio_v2,error_codes,keepalive,input_block_local,display_modes,blank_screen,key_map_mac,chat"
	if got := featureList(m); got != want {
		t.Fatalf("features %s, want %s", got, want)
	}
	if c := m["clipboard"].(map[string]interface{}); c["text"] != true || c["files"] != true {
		t.Fatalf("clipboard caps %v", c)
	}
	if b := m["blankScreen"].(map[string]interface{}); b["available"] != true {
		t.Fatalf("blank screen %v", b)
	}
}

func TestGateInitClipboardWithoutFiles(t *testing.T) {
	m := gateOutInit(t, Options{AllowClipboard: true})
	if got, want := featureList(m), "view_gen,mosaic,clipboard_text,clipboard_push,audio_v2,error_codes,keepalive"; got != want {
		t.Fatalf("features %s, want %s", got, want)
	}
	if c := m["clipboard"].(map[string]interface{}); c["text"] != true || c["files"] != false {
		t.Fatalf("clipboard caps %v", c)
	}
}

func TestGateOutbound(t *testing.T) {
	var starts int32
	c := newMemConn()
	g := newGate(c, Options{OnStart: func() { atomic.AddInt32(&starts, 1) }})
	send := []memFrame{
		txt(`{"type":"clipboard_content","text":"secret"}`),
		txt(`{"type":"clipboard_files","files":[]}`),
		txt(`{"type":"file_offer","tid":1}`),
		txt(`{"type":"file_error","tid":1,"code":"forbidden"}`),
		txt(`{"type":"input_desktop","desktop":"Winlogon"}`),
		txt(`{"type":"codec_switch","codec":"jpeg"}`),
		{0x2, []byte{frameTypeFileDown, 0, 0, 0, 0, 0, 0, 0, 0}},
		{0x2, []byte{FrameTypeH264, 1, 2, 3}},
		txt(fullInit),
		txt(fullInit),
	}
	for _, f := range send {
		if err := g.WriteFrame(f.op, f.p); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(types(c.pending()), ",")
	if want := "file_error,codec_switch,bin,init,init"; got != want {
		t.Fatalf("sent %s, want %s", got, want)
	}
	if starts != 1 {
		t.Fatalf("OnStart ran %d times", starts)
	}
	// Everything allowed: only the vault's input_desktop is held back.
	c2 := newMemConn()
	g2 := newGate(c2, allowAll)
	for _, f := range send[:8] {
		_ = g2.WriteFrame(f.op, f.p)
	}
	if got := strings.Join(types(c2.pending()), ","); got != "clipboard_content,clipboard_files,file_offer,file_error,codec_switch,bin,bin" {
		t.Fatalf("allow all: sent %s", got)
	}
}

func TestSendChat(t *testing.T) {
	c := newMemConn()
	s := NewSession(c, Options{})
	if err := s.SendChat("hi"); err != ErrChatDisabled {
		t.Fatalf("chat off: %v", err)
	}
	s = NewSession(c, Options{OnChat: func(string) {}})
	if err := s.SendChat("héllo"); err != nil {
		t.Fatal(err)
	}
	if f := c.next(t, time.Second); f.op != 0x1 || string(f.p) != `{"text":"héllo","type":"chat"}` {
		t.Fatalf("chat frame %d %s", f.op, f.p)
	}
	if got := cutUTF8("aé", 2); got != "a" {
		t.Fatalf("cutUTF8 = %q", got)
	}
}
