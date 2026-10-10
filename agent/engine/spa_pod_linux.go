//go:build linux

package engine

// SPA pods (the PipeWire parameter format) built in pure Go, so the
// negotiation parameters are unit-tested without libspa: spa_pod_test.go
// checks the bytes against what the libspa builder macros produce. The
// layout is libspa's stable ABI (spa/pod/pod.h): every pod is
// {u32 size, u32 type, body}, padded to 8 bytes; an object body is
// {u32 object type, u32 param id, props...}, a prop {u32 key, u32 flags,
// pod}, a choice body {u32 choice type, u32 flags, u32 child size, u32 child
// type, values...}.

import "encoding/binary"

// SPA constants used here (spa/utils/type.h, spa/param/*.h).
const (
	spaTypeID        = 3
	spaTypeInt       = 4
	spaTypeLong      = 5
	spaTypeRectangle = 10
	spaTypeFraction  = 11
	spaTypeObject    = 15
	spaTypeChoice    = 19

	spaTypeObjectFormat       = 0x40003
	spaTypeObjectParamBuffers = 0x40004
	spaTypeObjectParamMeta    = 0x40005

	spaParamEnumFormat = 3
	spaParamFormat     = 4
	spaParamBuffers    = 5
	spaParamMeta       = 6

	spaChoiceNone  = 0
	spaChoiceRange = 1
	spaChoiceEnum  = 3
	spaChoiceFlags = 4

	spaFormatMediaType      = 1
	spaFormatMediaSubtype   = 2
	spaFormatVideoFormat    = 0x20001
	spaFormatVideoModifier  = 0x20002
	spaFormatVideoSize      = 0x20003
	spaFormatVideoFramerate = 0x20004
	spaMediaTypeVideo       = 2
	spaMediaSubtypeRaw      = 1
	spaParamBuffersDataType = 6
	spaParamMetaType        = 1
	spaParamMetaSize        = 2

	spaDataMemPtr = 1
	spaDataMemFd  = 2
	spaDataDmaBuf = 3

	spaMetaHeader = 1
	spaMetaCursor = 5

	spaVideoFormatRGBx = 7
	spaVideoFormatBGRx = 8
	spaVideoFormatxRGB = 9
	spaVideoFormatxBGR = 10
	spaVideoFormatRGBA = 11
	spaVideoFormatBGRA = 12
	spaVideoFormatARGB = 13
	spaVideoFormatABGR = 14

	// sizeof(struct spa_meta_header), spa_meta_cursor, spa_meta_bitmap
	// (checked against the headers by the build-host test).
	spaMetaHeaderSize = 32
	spaMetaCursorSize = 28
	spaMetaBitmapSize = 20
)

// spaCursorMetaSize: room for a cursor meta with a w×h 32-bit bitmap.
func spaCursorMetaSize(w, h int) int32 {
	return int32(spaMetaCursorSize + spaMetaBitmapSize + w*h*4)
}

type spaPod []byte

func pad8(n int) int { return (n + 7) &^ 7 }

func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// spaPrimitive: a pod of type t around body (padded).
func spaPrimitive(t uint32, body []byte) spaPod {
	out := make([]byte, 0, 8+pad8(len(body)))
	out = le32(out, uint32(len(body)))
	out = le32(out, t)
	out = append(out, body...)
	for len(out)%8 != 0 {
		out = append(out, 0)
	}
	return out
}

func spaID(v uint32) spaPod { return spaPrimitive(spaTypeID, le32(nil, v)) }
func spaInt(v int32) spaPod { return spaPrimitive(spaTypeInt, le32(nil, uint32(v))) }

// spaChoice: a choice of kind over values of childType (each childSize
// bytes, first one = default).
func spaChoice(kind, childType, childSize uint32, values ...[]byte) spaPod {
	body := le32(nil, kind)
	body = le32(body, 0) // flags
	body = le32(body, childSize)
	body = le32(body, childType)
	for _, v := range values {
		body = append(body, v...)
	}
	return spaPrimitive(spaTypeChoice, body)
}

func spaRectVal(w, h uint32) []byte { return le32(le32(nil, w), h) }
func spaFracVal(n, d uint32) []byte { return le32(le32(nil, n), d) }
func spaU32Val(v uint32) []byte     { return le32(nil, v) }

type spaProp struct {
	key, flags uint32
	value      spaPod
}

func spaObject(objType, id uint32, props ...spaProp) spaPod {
	body := le32(nil, objType)
	body = le32(body, id)
	for _, p := range props {
		body = le32(body, p.key)
		body = le32(body, p.flags)
		body = append(body, p.value...)
	}
	return spaPrimitive(spaTypeObject, body)
}

// pwCaptureFormats are the raw formats we accept, most wanted first (BGRx is
// what the encoders and JPEG take as is; the others are converted).
var pwCaptureFormats = []uint32{
	spaVideoFormatBGRx, spaVideoFormatBGRA, spaVideoFormatRGBx, spaVideoFormatRGBA,
	spaVideoFormatxRGB, spaVideoFormatARGB, spaVideoFormatxBGR, spaVideoFormatABGR,
}

// pwEnumFormatPod is the EnumFormat parameter of a capture stream: raw video
// in one of pwCaptureFormats, any size, any frame rate. No modifier is
// offered: the compositor then hands out shared memory (MemFd / MemPtr), the
// path every compositor and driver supports, NVIDIA included (a DMA-BUF
// import through EGL is the next step: it needs the modifier list of the
// GPU).
func pwEnumFormatPod() spaPod {
	fmts := make([][]byte, 0, len(pwCaptureFormats)+1)
	fmts = append(fmts, spaU32Val(pwCaptureFormats[0])) // default
	for _, f := range pwCaptureFormats {
		fmts = append(fmts, spaU32Val(f))
	}
	return spaObject(spaTypeObjectFormat, spaParamEnumFormat,
		spaProp{key: spaFormatMediaType, value: spaID(spaMediaTypeVideo)},
		spaProp{key: spaFormatMediaSubtype, value: spaID(spaMediaSubtypeRaw)},
		spaProp{key: spaFormatVideoFormat, value: spaChoice(spaChoiceEnum, spaTypeID, 4, fmts...)},
		spaProp{key: spaFormatVideoSize, value: spaChoice(spaChoiceRange, spaTypeRectangle, 8,
			spaRectVal(1920, 1080), spaRectVal(1, 1), spaRectVal(8192, 8192))},
		spaProp{key: spaFormatVideoFramerate, value: spaChoice(spaChoiceRange, spaTypeFraction, 8,
			spaFracVal(30, 1), spaFracVal(0, 1), spaFracVal(360, 1))},
	)
}

// pwBuffersPod: buffers in shared memory only (MemFd or MemPtr), mapped by
// PipeWire (PW_STREAM_FLAG_MAP_BUFFERS).
func pwBuffersPod() spaPod {
	return spaObject(spaTypeObjectParamBuffers, spaParamBuffers,
		spaProp{key: spaParamBuffersDataType, value: spaChoice(spaChoiceFlags, spaTypeInt, 4,
			spaU32Val(1<<spaDataMemFd|1<<spaDataMemPtr))},
	)
}

// pwMetaHeaderPod asks for the buffer header (corrupted-frame flag).
func pwMetaHeaderPod() spaPod {
	return spaObject(spaTypeObjectParamMeta, spaParamMeta,
		spaProp{key: spaParamMetaType, value: spaID(spaMetaHeader)},
		spaProp{key: spaParamMetaSize, value: spaInt(spaMetaHeaderSize)},
	)
}

// pwMetaCursorPod asks for the pointer as metadata (cursor_mode metadata):
// up to 256×256 bitmaps, 64×64 preferred.
func pwMetaCursorPod() spaPod {
	return spaObject(spaTypeObjectParamMeta, spaParamMeta,
		spaProp{key: spaParamMetaType, value: spaID(spaMetaCursor)},
		spaProp{key: spaParamMetaSize, value: spaChoice(spaChoiceRange, spaTypeInt, 4,
			spaU32Val(uint32(spaCursorMetaSize(64, 64))), spaU32Val(uint32(spaCursorMetaSize(1, 1))),
			spaU32Val(uint32(spaCursorMetaSize(256, 256))))},
	)
}

// spaFormatInfo is what a negotiated Format parameter says.
type spaFormatInfo struct {
	Format      uint32
	Width       uint32
	Height      uint32
	HasModifier bool
}

// parseSpaFormat reads a Format object (pure Go twin of the C parser in
// pipewire_linux.go, used by the tests to check both agree).
func parseSpaFormat(p []byte) (spaFormatInfo, bool) {
	var fi spaFormatInfo
	if len(p) < 16 {
		return fi, false
	}
	size := int(binary.LittleEndian.Uint32(p[0:]))
	if binary.LittleEndian.Uint32(p[4:]) != spaTypeObject || 8+size > len(p) || size < 8 {
		return fi, false
	}
	end := 8 + size
	off := 16
	for off+16 <= end {
		key := binary.LittleEndian.Uint32(p[off:])
		vsize := int(binary.LittleEndian.Uint32(p[off+8:]))
		vtype := binary.LittleEndian.Uint32(p[off+12:])
		body := off + 16
		if body+vsize > end {
			return fi, false
		}
		val, vt, vs := body, vtype, vsize
		if vtype == spaTypeChoice && vsize >= 16 {
			vs = int(binary.LittleEndian.Uint32(p[body+8:]))
			vt = binary.LittleEndian.Uint32(p[body+12:])
			val = body + 16
			if val+vs > body+vsize {
				vs = 0
			}
		}
		switch key {
		case spaFormatVideoFormat:
			if vt == spaTypeID && vs >= 4 {
				fi.Format = binary.LittleEndian.Uint32(p[val:])
			}
		case spaFormatVideoSize:
			if vt == spaTypeRectangle && vs >= 8 {
				fi.Width = binary.LittleEndian.Uint32(p[val:])
				fi.Height = binary.LittleEndian.Uint32(p[val+4:])
			}
		case spaFormatVideoModifier:
			fi.HasModifier = true
		}
		off = body + pad8(vsize)
	}
	return fi, fi.Format != 0 && fi.Width > 0 && fi.Height > 0
}
