package engine

import (
	"errors"
	"fmt"
)

// Minimal AV1 bitstream reader for the encoder tests: splits a temporal unit
// in the low-overhead OBU format (AV1 spec 5.2, what WebCodecs decodes
// without a description) and reads the sequence header up to the colour
// configuration (5.5) and the first bits of the frame header (5.9).

const (
	obuSequenceHeader       = 1
	obuTemporalDelimiter    = 2
	obuFrameHeader          = 3
	obuTileGroup            = 4
	obuMetadata             = 5
	obuFrame                = 6
	obuRedundantFrameHeader = 7
	obuPadding              = 15
)

type av1SeqHeader struct {
	profile       int
	reducedStill  bool
	width, height int
	bitDepth      int
	mono          bool
}

type av1TemporalUnit struct {
	types    []int
	seq      *av1SeqHeader
	hasFrame bool
	key      bool // the first frame (header) OBU is a shown key frame
}

type av1Bits struct {
	b   []byte
	pos int // in bits
}

func (r *av1Bits) f(n int) (int, error) {
	v := 0
	for i := 0; i < n; i++ {
		if r.pos >= len(r.b)*8 {
			return 0, errors.New("sequence header truncated")
		}
		bit := (r.b[r.pos/8] >> (7 - uint(r.pos%8))) & 1
		v = v<<1 | int(bit)
		r.pos++
	}
	return v, nil
}

func (r *av1Bits) uvlc() error {
	zeros := 0
	for {
		b, err := r.f(1)
		if err != nil {
			return err
		}
		if b == 1 {
			break
		}
		zeros++
		if zeros >= 32 {
			return errors.New("uvlc too long")
		}
	}
	_, err := r.f(zeros)
	return err
}

func parseAV1TemporalUnit(b []byte) (av1TemporalUnit, error) {
	var tu av1TemporalUnit
	if len(b) == 0 {
		return tu, errors.New("empty temporal unit")
	}
	for i := 0; i < len(b); {
		hdr := b[i]
		if hdr&0x80 != 0 {
			return tu, fmt.Errorf("OBU at %d: forbidden bit set", i)
		}
		typ := int(hdr>>3) & 0x0f
		ext := hdr&0x04 != 0
		if hdr&0x02 == 0 {
			return tu, fmt.Errorf("OBU at %d: no obu_has_size_field (not the low-overhead format)", i)
		}
		p := i + 1
		if ext {
			p++
		}
		size, n := 0, 0
		for {
			if p >= len(b) || n == 8 {
				return tu, fmt.Errorf("OBU at %d: truncated size", i)
			}
			c := b[p]
			p++
			size |= int(c&0x7f) << (7 * n)
			n++
			if c&0x80 == 0 {
				break
			}
		}
		if size < 0 || p+size > len(b) {
			return tu, fmt.Errorf("OBU at %d (type %d): size %d past the end (%d bytes)", i, typ, size, len(b))
		}
		payload := b[p : p+size]
		tu.types = append(tu.types, typ)
		switch typ {
		case obuSequenceHeader:
			s, err := parseAV1SeqHeader(payload)
			if err != nil {
				return tu, err
			}
			tu.seq = &s
		case obuTemporalDelimiter:
			if size != 0 || len(tu.types) != 1 {
				return tu, errors.New("temporal delimiter not first or not empty")
			}
		case obuFrameHeader, obuFrame:
			if !tu.hasFrame {
				if size == 0 {
					return tu, errors.New("empty frame header")
				}
				if tu.seq != nil && tu.seq.reducedStill {
					tu.key = true
				} else {
					// show_existing_frame (1), frame_type (2): KEY_FRAME = 0.
					tu.key = payload[0]&0x80 == 0 && (payload[0]>>5)&0x03 == 0
				}
			}
			tu.hasFrame = true
		case obuTileGroup, obuMetadata, obuRedundantFrameHeader, obuPadding:
		default:
			return tu, fmt.Errorf("OBU at %d: unexpected type %d", i, typ)
		}
		i = p + size
	}
	return tu, nil
}

func parseAV1SeqHeader(b []byte) (av1SeqHeader, error) {
	var s av1SeqHeader
	r := &av1Bits{b: b}
	var err error
	must := func(n int) int {
		if err != nil {
			return 0
		}
		var v int
		v, err = r.f(n)
		return v
	}
	s.profile = must(3)
	must(1) // still_picture
	s.reducedStill = must(1) == 1
	decoderModel, bufferDelayLen := false, 0
	if s.reducedStill {
		must(5) // seq_level_idx[0]
	} else {
		if must(1) == 1 { // timing_info_present_flag
			must(32) // num_units_in_display_tick
			must(32) // time_scale
			if must(1) == 1 && err == nil {
				err = r.uvlc() // num_ticks_per_picture_minus_1
			}
			if must(1) == 1 { // decoder_model_info_present_flag
				decoderModel = true
				bufferDelayLen = must(5) + 1
				must(32) // num_units_in_decoding_tick
				must(5)  // buffer_removal_time_length_minus_1
				must(5)  // frame_presentation_time_length_minus_1
			}
		}
		initialDisplayDelay := must(1) == 1
		ops := must(5) + 1
		for i := 0; i < ops && err == nil; i++ {
			must(12) // operating_point_idc
			if must(5) > 7 {
				must(1) // seq_tier
			}
			if decoderModel && must(1) == 1 {
				must(bufferDelayLen) // decoder_buffer_delay
				must(bufferDelayLen) // encoder_buffer_delay
				must(1)              // low_delay_mode_flag
			}
			if initialDisplayDelay && must(1) == 1 {
				must(4)
			}
		}
	}
	wBits := must(4) + 1
	hBits := must(4) + 1
	s.width = must(wBits) + 1
	s.height = must(hBits) + 1
	if !s.reducedStill && must(1) == 1 { // frame_id_numbers_present_flag
		must(4)
		must(3)
	}
	must(3) // use_128x128_superblock, enable_filter_intra, enable_intra_edge
	if !s.reducedStill {
		must(4) // interintra, masked, warped, dual_filter
		orderHint := must(1) == 1
		if orderHint {
			must(2) // jnt_comp, ref_frame_mvs
		}
		forceSCT := 2
		if must(1) == 0 { // seq_choose_screen_content_tools
			forceSCT = must(1)
		}
		if forceSCT > 0 && must(1) == 0 { // seq_choose_integer_mv
			must(1)
		}
		if orderHint {
			must(3)
		}
	}
	must(3) // enable_superres, enable_cdef, enable_restoration
	// color_config
	s.bitDepth = 8
	if must(1) == 1 { // high_bitdepth
		s.bitDepth = 10
		if s.profile == 2 && must(1) == 1 {
			s.bitDepth = 12
		}
	}
	if s.profile != 1 {
		s.mono = must(1) == 1
	}
	if err != nil {
		return s, err
	}
	return s, nil
}
