//go:build linux

package engine

// H.264 on Linux: one interface, several backends, the best available one
// chosen at run time. Every library is loaded with dlopen, so a missing one
// only removes its backend (down to JPEG).
//
//	nvenc     NVIDIA NVENC through GStreamer's nvcodec plugin (nvcudah264enc /
//	          nvh264enc), present when the NVIDIA driver (and, in a Flatpak,
//	          the org.freedesktop.Platform.GL.nvidia extension) provides
//	          libnvidia-encode. NVIDIA has no VA-API encoder.
//	vaapi     VA-API (AMD / Intel) through GStreamer's va plugin (vah264lpenc,
//	          vah264enc) or the older gstreamer-vaapi (vaapih264enc).
//	openh264  Cisco OpenH264, software (encode_openh264_linux.go): the
//	          libopenh264.so.7 of the system / of the Flatpak extension
//	          org.freedesktop.Platform.openh264, or the binary downloaded from
//	          Cisco by FetchOpenH264 (SHA-256 pinned).
//
// Order: hardware first (a remote-help session must not load the user's CPU),
// OpenH264 next. OBLIREACH_H264_ENCODER (or SetLinuxH264Encoder) forces one
// backend ("nvenc", "vaapi", "openh264") or none ("none": JPEG).
//
// A direct NVENC backend (libnvidia-encode.so.1 by dlopen,
// NvEncodeAPICreateInstance, NV_ENC_INPUT_PTR from system memory, later from
// the DMA-BUF / CUDA import) is the planned replacement for the GStreamer
// detour where the nvcodec plugin is missing: it implements h264Encoder
// and is registered in linuxH264Backends before "nvenc".
//
// stream.go knows one H.264 slot by the name of the Windows OpenH264
// functions (openH264Available / Init / EncodeFrame / SetBitrate / Close):
// on Linux those names are this facade.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

// h264Encoder is one H.264 backend instance. encode returns one Annex B
// access unit (nil when the encoder produced nothing for this frame).
type h264Encoder interface {
	init(w, h, fps, bitrate int) error
	encode(bgra []byte, w, h int, tsMs int64) ([]byte, error)
	setBitrate(bps int)
	close()
}

// h264Backend describes a backend: probe says whether it can run here (nil:
// yes), create makes an instance.
type h264Backend struct {
	name     string
	hardware bool
	probe    func() error
	create   func() h264Encoder
}

// linuxH264Backends, in the default order.
var linuxH264Backends = []h264Backend{
	{name: "nvenc", hardware: true, probe: func() error { return gstProbe(gstFamilyNVENC) },
		create: func() h264Encoder { return newGstEncoder(gstFamilyNVENC) }},
	{name: "vaapi", hardware: true, probe: func() error { return gstProbe(gstFamilyVAAPI) },
		create: func() h264Encoder { return newGstEncoder(gstFamilyVAAPI) }},
	{name: "openh264", probe: openH264LinuxProbe,
		create: func() h264Encoder { return &openH264Linux{} }},
}

var linuxH264Pref struct {
	mu   sync.Mutex
	name string
}

// SetLinuxH264Encoder chooses the H.264 encoder: "auto" (default),
// "nvenc", "vaapi", "openh264" or "none" (JPEG only). The
// OBLIREACH_H264_ENCODER environment variable wins (diagnostics).
func SetLinuxH264Encoder(name string) {
	linuxH264Pref.mu.Lock()
	linuxH264Pref.name = name
	linuxH264Pref.mu.Unlock()
	h264Probe.reset()
}

// orderH264Backends: the backend names to try, in order, for a preference
// and the set of available backends (pure).
func orderH264Backends(pref string, all []h264Backend, available map[string]bool) []string {
	pref = strings.ToLower(strings.TrimSpace(pref))
	switch pref {
	case "none", "off", "jpeg":
		return nil
	case "", "auto":
	default:
		for _, b := range all {
			if b.name == pref && available[b.name] {
				return []string{b.name}
			}
		}
		return nil // forced and unavailable: JPEG, never another encoder
	}
	var out []string
	for _, hw := range []bool{true, false} {
		for _, b := range all {
			if b.hardware == hw && available[b.name] {
				out = append(out, b.name)
			}
		}
	}
	return out
}

func linuxH264Preference() string {
	if v := strings.TrimSpace(os.Getenv("OBLIREACH_H264_ENCODER")); v != "" {
		return v
	}
	linuxH264Pref.mu.Lock()
	defer linuxH264Pref.mu.Unlock()
	return linuxH264Pref.name
}

// h264Probe caches which backends can run (probing loads libraries).
var h264Probe probeCache

type probeCache struct {
	mu    sync.Mutex
	done  bool
	avail map[string]bool
}

func (p *probeCache) reset() {
	p.mu.Lock()
	p.done = false
	p.mu.Unlock()
}

func (p *probeCache) get() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.done {
		p.avail = map[string]bool{}
		for _, b := range linuxH264Backends {
			if err := b.probe(); err != nil {
				log.Printf("encoder: %s unavailable: %v", b.name, err)
			} else {
				p.avail[b.name] = true
			}
		}
		p.done = true
	}
	return p.avail
}

// LinuxH264Encoders lists the H.264 encoders usable here, in the order they
// would be tried (diagnostics, the host's settings page).
func LinuxH264Encoders() []string {
	return orderH264Backends(linuxH264Preference(), linuxH264Backends, h264Probe.get())
}

// Runtime fallback: a backend that keeps failing (or never outputs) is
// replaced by the next one of the order.
const (
	h264FailLimit   = 10 // consecutive encode errors
	h264SilentLimit = 30 // frames without any output
)

var linuxH264 struct {
	mu       sync.Mutex
	enc      h264Encoder
	name     string
	order    []string
	pos      int
	fails    int
	frames   int
	produced int
	w, h     int
	fps, br  int
}

func backendByName(name string) *h264Backend {
	for i := range linuxH264Backends {
		if linuxH264Backends[i].name == name {
			return &linuxH264Backends[i]
		}
	}
	return nil
}

func openH264Available() bool {
	return len(orderH264Backends(linuxH264Preference(), linuxH264Backends, h264Probe.get())) > 0
}

func openH264Init(width, height, fps, bitrate int) error {
	linuxH264.mu.Lock()
	defer linuxH264.mu.Unlock()
	closeLinuxH264Locked()
	linuxH264.order = orderH264Backends(linuxH264Preference(), linuxH264Backends, h264Probe.get())
	linuxH264.w, linuxH264.h, linuxH264.fps, linuxH264.br = width, height, fps, bitrate
	return startLinuxH264Locked(0)
}

// startLinuxH264Locked initialises the first backend from position pos on.
func startLinuxH264Locked(pos int) error {
	var errs []string
	for i := pos; i < len(linuxH264.order); i++ {
		b := backendByName(linuxH264.order[i])
		if b == nil {
			continue
		}
		enc := b.create()
		if err := enc.init(linuxH264.w, linuxH264.h, linuxH264.fps, linuxH264.br); err != nil {
			log.Printf("encoder: %s init failed: %v", b.name, err)
			errs = append(errs, b.name+": "+err.Error())
			continue
		}
		linuxH264.enc, linuxH264.name, linuxH264.pos = enc, b.name, i
		linuxH264.fails, linuxH264.frames, linuxH264.produced = 0, 0, 0
		log.Printf("encoder: H.264 via %s (%dx%d@%d, %d kbps)", b.name, linuxH264.w, linuxH264.h, linuxH264.fps, linuxH264.br/1000)
		return nil
	}
	if len(errs) == 0 {
		return errors.New("no H.264 encoder available")
	}
	return fmt.Errorf("no H.264 encoder could start (%s)", strings.Join(errs, "; "))
}

func closeLinuxH264Locked() {
	if linuxH264.enc != nil {
		linuxH264.enc.close()
		linuxH264.enc = nil
		linuxH264.name = ""
	}
}

func openH264EncodeFrame(bgra []byte, width, height int, ts int64) ([]byte, error) {
	linuxH264.mu.Lock()
	defer linuxH264.mu.Unlock()
	if linuxH264.enc == nil {
		return nil, errors.New("H.264 encoder not initialised")
	}
	out, err := linuxH264.enc.encode(bgra, width, height, ts)
	linuxH264.frames++
	if err != nil {
		linuxH264.fails++
	} else {
		linuxH264.fails = 0
		if len(out) > 0 {
			linuxH264.produced++
		}
	}
	if linuxH264.fails >= h264FailLimit || (linuxH264.produced == 0 && linuxH264.frames >= h264SilentLimit) {
		log.Printf("encoder: %s gave up (errors=%d, frames=%d, output=%d, last error: %v): next encoder",
			linuxH264.name, linuxH264.fails, linuxH264.frames, linuxH264.produced, err)
		next := linuxH264.pos + 1
		closeLinuxH264Locked()
		if serr := startLinuxH264Locked(next); serr != nil {
			return nil, serr
		}
		return nil, nil
	}
	return out, err
}

func openH264SetBitrate(bitrate int) {
	linuxH264.mu.Lock()
	defer linuxH264.mu.Unlock()
	linuxH264.br = bitrate
	if linuxH264.enc != nil {
		linuxH264.enc.setBitrate(bitrate)
	}
}

func openH264Close() {
	linuxH264.mu.Lock()
	defer linuxH264.mu.Unlock()
	closeLinuxH264Locked()
}

// ActiveLinuxH264Encoder names the encoder of the running stream ("" when
// none: JPEG).
func ActiveLinuxH264Encoder() string {
	linuxH264.mu.Lock()
	defer linuxH264.mu.Unlock()
	return linuxH264.name
}
