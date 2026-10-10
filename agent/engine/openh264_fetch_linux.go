//go:build linux

package engine

// Cisco's OpenH264 binary on Linux. Cisco pays the H.264 patent royalties
// for its own binary only when the END USER downloads it from Cisco: it is
// never shipped with Oblireach. FetchOpenH264 downloads it from Cisco on the
// user's machine (the host shows OpenH264Notice, as the license asks, and
// lets the user decline), checks it against the pinned SHA-256 and installs
// it in a folder of ours; the encoder loads only a file that matches the pin
// (openH264Candidates). The Flatpak extension
// org.freedesktop.Platform.openh264 is the other legitimate source (Cisco's
// binary, downloaded by Flatpak): it is picked up through the soname.

import (
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// OpenH264Notice is the attribution Cisco's license requires wherever the
// downloaded codec is offered.
const OpenH264Notice = "OpenH264 Video Codec provided by Cisco Systems, Inc."

// openH264Pin is one pinned Cisco build.
type openH264Pin struct {
	URL    string // Cisco's download (bzip2)
	File   string // installed file name
	BZ2SHA string // SHA-256 of the download
	SOSHA  string // SHA-256 of the library itself (what is checked before every load)
}

// openH264LinuxPins: UPDATE WITH THE WINDOWS PIN (codec_pins.go) when the
// OpenH264 version moves. Hashes computed from Cisco's files on 2026-10-05:
//
//	curl -O https://ciscobinary.openh264.org/libopenh264-2.4.1-linux64.7.so.bz2
//	sha256sum libopenh264-2.4.1-linux64.7.so.bz2; bunzip2 -k ...; sha256sum libopenh264-2.4.1-linux64.7.so
var openH264LinuxPins = map[string]openH264Pin{
	"amd64": {
		URL:    "https://ciscobinary.openh264.org/libopenh264-2.4.1-linux64.7.so.bz2",
		File:   "libopenh264-2.4.1-linux64.7.so",
		BZ2SHA: "ca413853d99d960ebcd5ae5b4c65a85bb2b5598e9042e64700a9f4b737ca3a3f",
		SOSHA:  "1392d21466bc638e68151b716d5b2086d54cd812afd43253f1adb5b6e0185f51",
	},
	// arm64 (libopenh264-2.4.1-linux-arm64.7.so.bz2): to pin when an arm64
	// build is made.
}

const openH264MaxDownload = 16 << 20 // the library is ~1.5 MB

var openH264DirSetting struct {
	mu  sync.Mutex
	dir string
}

// SetOpenH264Dir sets the folder FetchOpenH264 installs into and the
// encoder loads from ("" = DefaultOpenH264Dir).
func SetOpenH264Dir(dir string) {
	openH264DirSetting.mu.Lock()
	openH264DirSetting.dir = dir
	openH264DirSetting.mu.Unlock()
	h264Probe.reset()
}

// DefaultOpenH264Dir: $XDG_DATA_HOME/oblireach/codecs (inside a Flatpak,
// the app's private data folder).
func DefaultOpenH264Dir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "oblireach", "codecs")
}

func openH264Dir() string {
	openH264DirSetting.mu.Lock()
	defer openH264DirSetting.mu.Unlock()
	if openH264DirSetting.dir != "" {
		return openH264DirSetting.dir
	}
	return DefaultOpenH264Dir()
}

// openH264Candidate is one library to try; verify (nil for a soname)
// checks the file before it is loaded.
type openH264Candidate struct {
	path   string
	verify func() error
}

func openH264Candidates() []openH264Candidate {
	var out []openH264Candidate
	if pin, ok := openH264LinuxPins[runtime.GOARCH]; ok {
		if dir := openH264Dir(); dir != "" {
			p := filepath.Join(dir, pin.File)
			if _, err := os.Stat(p); err == nil {
				sha := pin.SOSHA
				out = append(out, openH264Candidate{path: p, verify: func() error { return verifyFileSHA256(p, sha) }})
			}
		}
	}
	// System / Flatpak extension (Cisco's build or the noopenh264 stub,
	// which the loader rejects).
	out = append(out, openH264Candidate{path: "libopenh264.so.7"})
	return out
}

// verifyFileSHA256 fails unless the file's SHA-256 is want.
func verifyFileSHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, openH264MaxDownload)); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("%s: SHA-256 %s does not match the pinned build", filepath.Base(path), got)
	}
	return nil
}

// OpenH264Installed reports whether the pinned Cisco build is in the codec
// folder (and intact).
func OpenH264Installed() bool {
	pin, ok := openH264LinuxPins[runtime.GOARCH]
	if !ok {
		return false
	}
	return verifyFileSHA256(filepath.Join(openH264Dir(), pin.File), pin.SOSHA) == nil
}

// FetchOpenH264 downloads Cisco's OpenH264 into dir ("" = the codec folder)
// and returns the installed path. Call it only after the user agreed (show
// OpenH264Notice). Nothing is installed unless both hashes match.
func FetchOpenH264(ctx context.Context, dir string) (string, error) {
	return fetchOpenH264(ctx, dir, http.DefaultClient, runtime.GOARCH)
}

func fetchOpenH264(ctx context.Context, dir string, client *http.Client, arch string) (string, error) {
	pin, ok := openH264LinuxPins[arch]
	if !ok {
		return "", fmt.Errorf("no pinned OpenH264 build for %s", arch)
	}
	if dir == "" {
		dir = openH264Dir()
	}
	if dir == "" {
		return "", errors.New("no folder for the codec")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, pin.File)
	if verifyFileSHA256(dst, pin.SOSHA) == nil {
		return dst, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenH264 download: HTTP %d", resp.StatusCode)
	}
	bz, err := io.ReadAll(io.LimitReader(resp.Body, openH264MaxDownload+1))
	if err != nil {
		return "", err
	}
	if len(bz) > openH264MaxDownload {
		return "", errors.New("OpenH264 download: too large")
	}
	if got := sha256Hex(bz); !strings.EqualFold(got, pin.BZ2SHA) {
		return "", fmt.Errorf("OpenH264 download: SHA-256 %s does not match the pinned build", got)
	}
	lib, err := io.ReadAll(io.LimitReader(bzip2.NewReader(bytes.NewReader(bz)), openH264MaxDownload+1))
	if err != nil {
		return "", fmt.Errorf("OpenH264 download: %w", err)
	}
	if got := sha256Hex(lib); !strings.EqualFold(got, pin.SOSHA) {
		return "", fmt.Errorf("OpenH264 library: SHA-256 %s does not match the pinned build", got)
	}
	tmp, err := os.CreateTemp(dir, ".openh264-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(lib)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmpName, dst)
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return "", werr
	}
	h264Probe.reset()
	return dst, nil
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
