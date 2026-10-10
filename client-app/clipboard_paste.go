package main

// clipboard_paste.go — Ctrl+V in a file explorer (explorer_viewer.go): the
// operator's clipboard pasted into the device's folder on screen.
//
// What the clipboard may hold (read on Windows by clipboard_paste_windows.go):
//   - files and folders copied in Windows Explorer (CF_HDROP): the folders
//     walked here (links never followed, the app's own data never read, the
//     batch limits of transfers.go: 10,000 entries, 32 levels, 100 MB per
//     file and 10 GiB in all, or no size limit for a transfer streamed to a
//     local SSH connection). Files "cut" in Explorer (Preferred DropEffect =
//     move) are COPIED all the same: nothing here ever deletes a local file;
//   - an image alone (a screenshot: no text with it): its PNG as is, else
//     its DIB (CF_DIBV5, CF_DIB) converted to PNG here, offered as
//     "Clipboard YYYY-MM-DD HH-MM-SS.png";
//   - anything else (text...): nothing to paste.
//
// The page never gets a path: each file is an opaque random handle of this
// paste (pasteStore), read in chunks through reachReadFileChunk like the
// ObliReach viewer's copied files, then sent by the transfer queue (the
// explorer's gated path: transfers.go, the same as a drop).

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pasteMaxSets   = 16             // pastes whose handles stay readable
	pasteTTL       = 12 * time.Hour // ... at most this long
	pasteMaxDIB    = 256 << 20      // a clipboard image, in bytes
	pasteMaxPixels = 1 << 28        // ... in pixels
	pasteMaxSide   = 1 << 15        // ... its width or height
)

// ── The result handed to the page ──

type pasteItem struct {
	Rel      string `json:"rel"` // "folder/sub/file.txt" ('/' separators)
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified,omitempty"`
	Handle   string `json:"handle,omitempty"` // a file's (reachReadFileChunk)
}

type pasteResult struct {
	Kind       string        `json:"kind"` // "files", "image" or "none"
	Cut        bool          `json:"cut"`  // cut in Explorer: copied all the same
	Items      []pasteItem   `json:"items"`
	TotalBytes int64         `json:"totalBytes"`
	Skipped    fsWalkSkipped `json:"skipped"`
	Limit      string        `json:"limit,omitempty"` // "entries", "depth", "bytes": nothing sent
}

// pasteKinds: what a paste would do now (the explorer's menu item).
type pasteKinds struct {
	Files bool `json:"files"`
	Image bool `json:"image"`
	Text  bool `json:"text"`
}

// ── The handles of the pastes ──

type pasteFile struct {
	path string // a file of the disk...
	data []byte // ... or an image made here
	size int64
}

type pasteSet struct {
	created time.Time
	files   []pasteFile
}

type pasteStore struct {
	mu    sync.Mutex
	seq   uint64
	sets  map[string]*pasteSet
	order []string
	now   func() time.Time
}

func newPasteStore() *pasteStore {
	return &pasteStore{sets: map[string]*pasteSet{}, now: time.Now}
}

// clipPastes: every window's (the handles are random: one window cannot
// guess another's).
var clipPastes = newPasteStore()

// add keeps a paste's files; their handles in the same order.
func (s *pasteStore) add(files []pasteFile) ([]string, error) {
	id, err := randomHex16()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	for len(s.order) >= pasteMaxSets {
		delete(s.sets, s.order[0])
		s.order = s.order[1:]
	}
	s.sets[id] = &pasteSet{created: now, files: files}
	s.order = append(s.order, id)
	out := make([]string, len(files))
	for i := range files {
		out[i] = "p" + id + "-" + strconv.Itoa(i)
	}
	return out, nil
}

func (s *pasteStore) pruneLocked(now time.Time) {
	keep := s.order[:0]
	for _, id := range s.order {
		if now.Sub(s.sets[id].created) > pasteTTL {
			delete(s.sets, id)
			continue
		}
		keep = append(keep, id)
	}
	s.order = keep
}

func (s *pasteStore) lookup(handle string) (pasteFile, bool) {
	if len(handle) < 35 || handle[0] != 'p' || handle[33] != '-' {
		return pasteFile{}, false
	}
	i, err := strconv.Atoi(handle[34:])
	if err != nil || i < 0 {
		return pasteFile{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	set := s.sets[handle[1:33]]
	if set == nil || i >= len(set.files) {
		return pasteFile{}, false
	}
	return set.files[i], true
}

var errPasteHandle = errors.New("unknown file handle")

// readB64: length bytes of a pasted file from offset, base64 (a short read
// at its end).
func (s *pasteStore) readB64(handle string, offset int64, length int) (string, error) {
	f, ok := s.lookup(handle)
	if !ok {
		return "", errPasteHandle
	}
	if offset < 0 || length <= 0 || length > 1<<20 || offset > f.size {
		return "", errors.New("invalid range")
	}
	if f.data != nil {
		end := offset + int64(length)
		if end > int64(len(f.data)) {
			end = int64(len(f.data))
		}
		return base64.StdEncoding.EncodeToString(f.data[offset:end]), nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	buf := make([]byte, length)
	n, err := fh.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf[:n]), nil
}

// randomHex16: 16 random bytes in hex (a paste's id).
func randomHex16() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// ── Files and folders copied: walked ──

type pasteWalker struct {
	sys      localSys
	protectR []string // never read (the app's own data)
	maxFile  int64
	maxTotal int64

	res     *pasteResult
	files   []pasteFile
	idx     []int // the item of each file
	entries int
}

var errPasteLimit = errors.New("limit")

func (w *pasteWalker) protected(p string) bool {
	if len(w.protectR) == 0 {
		return false
	}
	if underAny(p, w.protectR) {
		return true
	}
	if f, err := w.sys.finalPath(p); err == nil && f != "" && underAny(f, w.protectR) {
		return true
	}
	return false
}

func (w *pasteWalker) limit(reason string) error {
	w.res.Limit = reason
	return errPasteLimit
}

func (w *pasteWalker) addDir(rel string, e localEntry) error {
	w.entries++
	if w.entries > localWalkCap {
		return w.limit("entries")
	}
	w.res.Items = append(w.res.Items, pasteItem{Rel: rel, IsDir: true, Modified: isoTime(e.Modified)})
	return nil
}

func (w *pasteWalker) addFile(abs, rel string, e localEntry) error {
	if e.Size > w.maxFile {
		w.res.Skipped.TooLarge++
		if len(w.res.Skipped.TooLargeNames) < 50 {
			w.res.Skipped.TooLargeNames = append(w.res.Skipped.TooLargeNames, rel)
		}
		return nil
	}
	w.entries++
	if w.entries > localWalkCap {
		return w.limit("entries")
	}
	w.res.TotalBytes += e.Size
	if w.res.TotalBytes > w.maxTotal {
		return w.limit("bytes")
	}
	w.idx = append(w.idx, len(w.res.Items))
	w.files = append(w.files, pasteFile{path: abs, size: e.Size})
	w.res.Items = append(w.res.Items, pasteItem{Rel: rel, Size: e.Size, Modified: isoTime(e.Modified)})
	return nil
}

func (w *pasteWalker) dir(abs, rel string, depth int) error {
	if depth > localWalkMaxDepth {
		return w.limit("depth")
	}
	if w.protected(abs) {
		w.res.Skipped.Denied++
		return nil
	}
	ents, err := w.sys.readDir(abs)
	if err != nil {
		w.res.Skipped.Denied++
		return nil
	}
	for _, e := range ents {
		r := rel + "/" + e.Name
		p := filepath.Join(abs, e.Name)
		switch {
		case e.Link:
			w.res.Skipped.Links++
		case e.IsDir:
			if w.protected(p) {
				w.res.Skipped.Denied++
				continue
			}
			if err := w.addDir(r, e); err != nil {
				return err
			}
			if err := w.dir(p, r, depth+1); err != nil {
				return err
			}
		default:
			if w.protected(p) {
				w.res.Skipped.Denied++
				continue
			}
			if err := w.addFile(p, r, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkPaths: the copied paths, each folder before its contents. A limit
// reached: the result says which, with no item (nothing is sent).
func (w *pasteWalker) walkPaths(paths []string) *pasteResult {
	w.res = &pasteResult{Kind: "files", Items: []pasteItem{}}
	for _, p := range paths {
		p = filepath.Clean(p)
		name := filepath.Base(p)
		if name == "" || name == "." || strings.ContainsAny(name, `\/:`) {
			w.res.Skipped.Denied++ // (a drive root, never pasted whole)
			continue
		}
		e, err := w.sys.lstat(p)
		if err != nil || w.protected(p) {
			w.res.Skipped.Denied++
			continue
		}
		var werr error
		switch {
		case e.Link:
			w.res.Skipped.Links++
		case e.IsDir:
			if werr = w.addDir(name, e); werr == nil {
				werr = w.dir(p, name, 1)
			}
		default:
			werr = w.addFile(p, name, e)
		}
		if werr != nil {
			w.res.Items = []pasteItem{}
			w.res.TotalBytes = 0
			w.files, w.idx = nil, nil
			return w.res
		}
	}
	return w.res
}

// pasteFiles: the copied paths walked, their files' handles registered.
func pasteFiles(sys localSys, protectR []string, paths []string, large bool) (*pasteResult, error) {
	w := &pasteWalker{sys: sys, protectR: protectR, maxFile: localMaxFileBytes, maxTotal: localWalkMaxBytes}
	if large {
		w.maxFile, w.maxTotal = localMaxStreamBytes, localMaxStreamBytes
	}
	res := w.walkPaths(paths)
	if len(w.files) > 0 {
		hs, err := clipPastes.add(w.files)
		if err != nil {
			return nil, err
		}
		for i, h := range hs {
			res.Items[w.idx[i]].Handle = h
		}
	}
	return res, nil
}

// ── An image: its PNG ──

// pasteImageName: "Clipboard 2026-10-04 14-05-09.png" (local time).
func pasteImageName(t time.Time) string {
	return "Clipboard " + t.Format("2006-01-02 15-04-05") + ".png"
}

// pasteImage: an image's PNG registered, the result for the page.
func pasteImage(pngData []byte, at time.Time) (*pasteResult, error) {
	hs, err := clipPastes.add([]pasteFile{{data: pngData, size: int64(len(pngData))}})
	if err != nil {
		return nil, err
	}
	n := int64(len(pngData))
	return &pasteResult{Kind: "image", TotalBytes: n,
		Items: []pasteItem{{Rel: pasteImageName(at), Size: n, Modified: isoTime(at), Handle: hs[0]}}}, nil
}

// pngChecked: a PNG of the clipboard ("PNG" format) as is, when it is one of
// a sane size.
func pngChecked(b []byte) bool {
	if len(b) == 0 || len(b) > pasteMaxDIB {
		return false
	}
	c, err := png.DecodeConfig(bytes.NewReader(b))
	return err == nil && c.Width > 0 && c.Height > 0 && c.Width <= pasteMaxSide && c.Height <= pasteMaxSide && c.Width*c.Height <= pasteMaxPixels
}

// dibToPNG: a packed DIB (CF_DIB, CF_DIBV5) as a PNG.
func dibToPNG(b []byte) ([]byte, error) {
	img, err := dibDecode(b)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var errDIB = errors.New("unsupported bitmap")

const (
	biRGB            = 0
	biBitfields      = 3
	biAlphaBitfields = 6
)

// maskChan: a channel of v under mask, on 8 bits.
func maskChan(v, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := bits.TrailingZeros32(mask)
	n := bits.OnesCount32(mask)
	x := (v & mask) >> uint(shift)
	if n >= 8 {
		return uint8(x >> uint(n-8))
	}
	return uint8(x * 255 / (1<<uint(n) - 1))
}

// dibDecode: a packed DIB (a BITMAPINFOHEADER, V4 or V5 header, its masks
// or palette, its pixels). Uncompressed only (1, 4, 8, 16, 24, 32 bits per
// pixel; BI_RGB, BI_BITFIELDS, BI_ALPHABITFIELDS).
func dibDecode(b []byte) (image.Image, error) {
	if len(b) < 40 || len(b) > pasteMaxDIB {
		return nil, errDIB
	}
	le32 := func(o int) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
	hs := int(le32(0))
	if hs < 40 || hs > len(b) {
		return nil, errDIB
	}
	w := int(int32(le32(4)))
	h := int(int32(le32(8)))
	planes := binary.LittleEndian.Uint16(b[12:])
	bpp := int(binary.LittleEndian.Uint16(b[14:]))
	comp := le32(16)
	clrUsed := int(le32(32))
	topDown := h < 0
	if topDown {
		h = -h
	}
	if planes != 1 || w <= 0 || h <= 0 || w > pasteMaxSide || h > pasteMaxSide || w*h > pasteMaxPixels {
		return nil, errDIB
	}
	off := hs
	var rm, gm, bm, am uint32
	switch comp {
	case biRGB:
		switch bpp {
		case 1, 4, 8, 24:
		case 16:
			rm, gm, bm = 0x7c00, 0x03e0, 0x001f
		case 32:
			rm, gm, bm, am = 0x00ff0000, 0x0000ff00, 0x000000ff, 0xff000000
		default:
			return nil, errDIB
		}
	case biBitfields, biAlphaBitfields:
		if bpp != 16 && bpp != 32 {
			return nil, errDIB
		}
		if hs >= 52 {
			rm, gm, bm = le32(40), le32(44), le32(48)
			if hs >= 56 {
				am = le32(52)
			}
		} else {
			n := 12
			if comp == biAlphaBitfields {
				n = 16
			}
			if len(b) < off+n {
				return nil, errDIB
			}
			rm, gm, bm = le32(off), le32(off+4), le32(off+8)
			if comp == biAlphaBitfields {
				am = le32(off + 12)
			}
			off += n
		}
	default:
		return nil, errDIB // (RLE, JPEG, PNG inside a DIB: not from a screenshot)
	}
	var pal []color.NRGBA
	if bpp <= 8 {
		n := clrUsed
		if n == 0 {
			n = 1 << uint(bpp)
		}
		if n > 256 || len(b) < off+4*n {
			return nil, errDIB
		}
		pal = make([]color.NRGBA, n)
		for i := range pal {
			p := b[off+4*i:]
			pal[i] = color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255}
		}
		off += 4 * n
	}
	stride := (w*bpp + 31) / 32 * 4
	need := stride * h
	// (some programs write the three masks after a V5 header too: skipped
	// when they are there)
	if (comp == biBitfields || comp == biAlphaBitfields) && hs >= 52 && len(b)-off-need >= 12 &&
		le32(off) == rm && le32(off+4) == gm && le32(off+8) == bm {
		off += 12
	}
	if len(b)-off < need {
		return nil, errDIB
	}
	px := b[off : off+need]
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	// 32 bits: an alpha channel only if some pixel uses it (a screenshot's
	// fourth byte is 0 everywhere: opaque).
	useAlpha := false
	if bpp == 32 && am != 0 {
		for i := 0; i+3 < len(px) && !useAlpha; i += 4 {
			if binary.LittleEndian.Uint32(px[i:])&am != 0 {
				useAlpha = true
			}
		}
	}
	for y := 0; y < h; y++ {
		sy := h - 1 - y
		if topDown {
			sy = y
		}
		row := px[sy*stride : sy*stride+stride]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			var c color.NRGBA
			switch bpp {
			case 1, 4, 8:
				bit := x * bpp
				v := int(row[bit/8]>>uint(8-bpp-bit%8)) & (1<<uint(bpp) - 1)
				if v < len(pal) {
					c = pal[v]
				} else {
					c = color.NRGBA{A: 255}
				}
			case 16:
				v := uint32(binary.LittleEndian.Uint16(row[2*x:]))
				c = color.NRGBA{R: maskChan(v, rm), G: maskChan(v, gm), B: maskChan(v, bm), A: 255}
			case 24:
				p := row[3*x:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255}
			case 32:
				v := binary.LittleEndian.Uint32(row[4*x:])
				c = color.NRGBA{R: maskChan(v, rm), G: maskChan(v, gm), B: maskChan(v, bm), A: 255}
				if useAlpha {
					c.A = maskChan(v, am)
				}
			}
			d := dst[4*x:]
			d[0], d[1], d[2], d[3] = c.R, c.G, c.B, c.A
		}
	}
	return img, nil
}
