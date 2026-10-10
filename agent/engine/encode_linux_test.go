//go:build linux

package engine

import (
	"errors"
	"strings"
	"testing"
)

func TestOrderH264Backends(t *testing.T) {
	all := []h264Backend{{name: "nvenc", hardware: true}, {name: "vaapi", hardware: true}, {name: "openh264"}}
	avail := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	cases := []struct {
		pref  string
		avail map[string]bool
		want  string
	}{
		{"", avail("nvenc", "vaapi", "openh264"), "nvenc vaapi openh264"},
		{"auto", avail("vaapi", "openh264"), "vaapi openh264"},
		{"", avail("openh264"), "openh264"},
		{"", avail(), ""},
		{"OpenH264", avail("nvenc", "openh264"), "openh264"},
		{"vaapi", avail("openh264"), ""}, // forced and missing: JPEG, never another
		{"none", avail("nvenc", "openh264"), ""},
		{"jpeg", avail("openh264"), ""},
	}
	for i, c := range cases {
		if got := strings.Join(orderH264Backends(c.pref, all, c.avail), " "); got != c.want {
			t.Errorf("case %d (%q): got %q, want %q", i, c.pref, got, c.want)
		}
	}
}

// fakeEnc is a scripted H.264 backend.
type fakeEnc struct {
	name    string
	initErr error
	fail    bool // every encode fails
	silent  bool // never outputs
	log     *[]string
}

func (f *fakeEnc) init(w, h, fps, br int) error {
	*f.log = append(*f.log, f.name+".init")
	return f.initErr
}
func (f *fakeEnc) encode(b []byte, w, h int, ts int64) ([]byte, error) {
	if f.fail {
		return nil, errors.New("boom")
	}
	if f.silent {
		return nil, nil
	}
	return []byte(f.name), nil
}
func (f *fakeEnc) setBitrate(bps int) { *f.log = append(*f.log, f.name+".bitrate") }
func (f *fakeEnc) close()             { *f.log = append(*f.log, f.name+".close") }

func withFakeBackends(t *testing.T, encs ...*fakeEnc) *[]string {
	t.Helper()
	log := &[]string{}
	saved := linuxH264Backends
	var bs []h264Backend
	for i, e := range encs {
		e := e
		e.log = log
		bs = append(bs, h264Backend{name: e.name, hardware: i < len(encs)-1,
			probe: func() error { return nil }, create: func() h264Encoder { return e }})
	}
	linuxH264Backends = bs
	h264Probe.reset()
	t.Setenv("OBLIREACH_H264_ENCODER", "")
	SetLinuxH264Encoder("auto")
	t.Cleanup(func() {
		openH264Close()
		linuxH264Backends = saved
		h264Probe.reset()
	})
	return log
}

func TestLinuxH264InitFallsThrough(t *testing.T) {
	log := withFakeBackends(t, &fakeEnc{name: "hw", initErr: errors.New("no device")}, &fakeEnc{name: "sw"})
	if !openH264Available() {
		t.Fatal("available")
	}
	if err := openH264Init(64, 64, 30, 1000000); err != nil {
		t.Fatal(err)
	}
	if ActiveLinuxH264Encoder() != "sw" {
		t.Errorf("active = %s", ActiveLinuxH264Encoder())
	}
	out, err := openH264EncodeFrame(make([]byte, 64*64*4), 64, 64, 0)
	if err != nil || string(out) != "sw" {
		t.Errorf("encode = %q %v", out, err)
	}
	openH264SetBitrate(500000)
	openH264Close()
	if got := strings.Join(*log, " "); got != "hw.init sw.init sw.bitrate sw.close" {
		t.Errorf("log = %s", got)
	}
}

func TestLinuxH264RuntimeFallback(t *testing.T) {
	_ = withFakeBackends(t, &fakeEnc{name: "hw", fail: true}, &fakeEnc{name: "sw"})
	if err := openH264Init(64, 64, 30, 1000000); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 64*64*4)
	for i := 0; i < h264FailLimit-1; i++ {
		if _, err := openH264EncodeFrame(frame, 64, 64, 0); err == nil {
			t.Fatal("hw must fail")
		}
	}
	if out, err := openH264EncodeFrame(frame, 64, 64, 0); out != nil || err != nil {
		t.Errorf("switch frame = %q %v", out, err)
	}
	if ActiveLinuxH264Encoder() != "sw" {
		t.Fatalf("active = %s", ActiveLinuxH264Encoder())
	}
	if out, _ := openH264EncodeFrame(frame, 64, 64, 0); string(out) != "sw" {
		t.Errorf("after fallback = %q", out)
	}
}

func TestLinuxH264SilentFallback(t *testing.T) {
	_ = withFakeBackends(t, &fakeEnc{name: "hw", silent: true}, &fakeEnc{name: "sw"})
	if err := openH264Init(64, 64, 30, 1000000); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 64*64*4)
	for i := 0; i < h264SilentLimit; i++ {
		_, _ = openH264EncodeFrame(frame, 64, 64, 0)
	}
	if ActiveLinuxH264Encoder() != "sw" {
		t.Errorf("a backend that never outputs must be replaced, active = %s", ActiveLinuxH264Encoder())
	}
}

func TestLinuxH264Forced(t *testing.T) {
	_ = withFakeBackends(t, &fakeEnc{name: "hw"}, &fakeEnc{name: "sw"})
	t.Setenv("OBLIREACH_H264_ENCODER", "sw")
	if err := openH264Init(64, 64, 30, 1000000); err != nil || ActiveLinuxH264Encoder() != "sw" {
		t.Errorf("forced: %v %s", err, ActiveLinuxH264Encoder())
	}
	t.Setenv("OBLIREACH_H264_ENCODER", "none")
	if openH264Available() {
		t.Error("none: JPEG only")
	}
	if err := openH264Init(64, 64, 30, 1000000); err == nil {
		t.Error("none: init must fail (stream.go then uses JPEG)")
	}
}

func TestGstPipelines(t *testing.T) {
	p := gstH264Pipeline("vah264lpenc", 1920, 1080, 30, 20000)
	for _, want := range []string{
		"appsrc name=src is-live=true", "format=BGRx,width=1920,height=1080,framerate=30/1",
		"videoconvert ! video/x-raw,format=NV12 ! vah264lpenc name=enc bitrate=20000 rate-control=cbr key-int-max=30 b-frames=0",
		"h264parse config-interval=-1 ! video/x-h264,stream-format=byte-stream,alignment=au",
		"appsink name=sink sync=false",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("va pipeline lacks %q:\n%s", want, p)
		}
	}
	n := gstH264Pipeline("nvh264enc", 1280, 720, 30, 8000)
	if !strings.Contains(n, "videoconvert ! nvh264enc name=enc bitrate=8000 rc-mode=cbr gop-size=30 bframes=0") {
		t.Errorf("nvenc pipeline:\n%s", n)
	}
	if c := gstEncoderProps("nvcudah264enc", 30, 5000); !strings.Contains(c, "tune=ultra-low-latency") || !strings.Contains(c, "b-frames=0") {
		t.Errorf("nvcuda props: %s", c)
	}
	for f, els := range gstFamilyElements {
		if len(els) == 0 {
			t.Errorf("%s: no element", f)
		}
		for _, el := range els {
			if strings.HasPrefix(gstEncoderProps(el, 30, 1), "bitrate=1") == false {
				t.Errorf("%s: bitrate first", el)
			}
		}
	}
}
