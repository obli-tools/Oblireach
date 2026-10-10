//go:build linux

package engine

// Pipelines of the GStreamer hardware encoders (encode_gst_linux.go), pure
// Go so their text is unit-tested. Frames come in as BGRx through appsrc,
// leave as one Annex B access unit per frame (SPS/PPS repeated on every IDR,
// what the viewers' WebCodecs decoder configures from) through appsink.

import (
	"fmt"
	"strings"
)

type gstFamily int

const (
	gstFamilyNVENC gstFamily = iota
	gstFamilyVAAPI
)

func (f gstFamily) String() string {
	if f == gstFamilyNVENC {
		return "nvenc"
	}
	return "vaapi"
}

// gstFamilyElements: the encoder elements of a family, preferred first.
// nvcodec registers its elements only when NVENC works on this machine, the
// va plugin only for a VA device that can encode H.264 (so "the element
// exists" is a good probe).
var gstFamilyElements = map[gstFamily][]string{
	gstFamilyNVENC: {"nvcudah264enc", "nvautogpuh264enc", "nvh264enc"},
	gstFamilyVAAPI: {"vah264lpenc", "vah264enc", "vaapih264enc"},
}

// gstRequiredElements are needed around every encoder.
var gstRequiredElements = []string{"appsrc", "appsink", "videoconvert", "h264parse"}

// gstEncoderProps: low-latency CBR settings of each element (no B-frames,
// one IDR per second). Bitrate in kbit/s, the unit of all of them. An
// unknown property only makes the parser warn (older plugin versions).
func gstEncoderProps(element string, fps, kbps int) string {
	switch element {
	case "nvcudah264enc", "nvautogpuh264enc":
		return fmt.Sprintf("bitrate=%d rate-control=cbr gop-size=%d b-frames=0 preset=p1 tune=ultra-low-latency zero-reorder-delay=true", kbps, fps)
	case "nvh264enc":
		return fmt.Sprintf("bitrate=%d rc-mode=cbr gop-size=%d bframes=0 preset=low-latency-hq zerolatency=true", kbps, fps)
	case "vah264lpenc", "vah264enc":
		return fmt.Sprintf("bitrate=%d rate-control=cbr key-int-max=%d b-frames=0 ref-frames=1 target-usage=6", kbps, fps)
	case "vaapih264enc":
		return fmt.Sprintf("bitrate=%d rate-control=cbr keyframe-period=%d max-bframes=0", kbps, fps)
	}
	return fmt.Sprintf("bitrate=%d", kbps)
}

// gstH264Pipeline is the gst_parse_launch description for one encoder.
func gstH264Pipeline(element string, w, h, fps, kbps int) string {
	conv := "videoconvert"
	if strings.HasPrefix(element, "va") {
		// VA encoders take NV12; the conversion runs on the CPU here (a
		// DMA-BUF import into vapostproc is the zero-copy follow-up).
		conv = "videoconvert ! video/x-raw,format=NV12"
	}
	return fmt.Sprintf("appsrc name=src is-live=true format=time do-timestamp=true block=false max-bytes=0 "+
		"caps=video/x-raw,format=BGRx,width=%d,height=%d,framerate=%d/1 ! %s ! %s name=enc %s ! "+
		"h264parse config-interval=-1 ! video/x-h264,stream-format=byte-stream,alignment=au ! "+
		"appsink name=sink sync=false max-buffers=8 drop=false",
		w, h, fps, conv, element, gstEncoderProps(element, fps, kbps))
}
