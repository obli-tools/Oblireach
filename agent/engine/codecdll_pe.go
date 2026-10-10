package engine

// Pure helpers of the verified codec DLL loader (codecdll_windows.go), kept
// platform-independent so they are unit-tested everywhere.

import (
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// codecDLLPinned reports whether data is one of the pinned builds of the DLL
// called name (case-insensitive), and its SHA-256.
func codecDLLPinned(name string, data []byte) (ok bool, sum string) {
	h := sha256.Sum256(data)
	sum = hex.EncodeToString(h[:])
	for _, p := range codecDLLPins[strings.ToLower(name)] {
		if p == sum {
			return true, sum
		}
	}
	return false, sum
}

// peImportDLLs lists the DLLs a PE image imports, both the import table and
// the delay-load table (lower case, in table order, without duplicates).
// debug/pe's ImportedLibraries is not implemented and ImportedSymbols skips
// ordinal-only imports, so the directories are walked here.
func peImportDLLs(data []byte) ([]string, error) {
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var dirs []pe.DataDirectory
	var imageBase uint64
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		dirs, imageBase = oh.DataDirectory[:dirCount(oh.NumberOfRvaAndSizes)], oh.ImageBase
	case *pe.OptionalHeader32:
		dirs, imageBase = oh.DataDirectory[:dirCount(oh.NumberOfRvaAndSizes)], uint64(oh.ImageBase)
	default:
		return nil, errors.New("PE: no optional header")
	}

	// off maps an RVA to a file offset inside a section's raw data.
	off := func(rva uint32) (int, bool) {
		for _, s := range f.Sections {
			if rva >= s.VirtualAddress && rva-s.VirtualAddress < s.Size {
				o := int(s.Offset) + int(rva-s.VirtualAddress)
				if o >= 0 && o < len(data) {
					return o, true
				}
			}
		}
		return 0, false
	}
	cstr := func(rva uint32) (string, error) {
		o, ok := off(rva)
		if !ok {
			return "", fmt.Errorf("PE: name RVA 0x%x outside the sections", rva)
		}
		end := bytes.IndexByte(data[o:], 0)
		if end < 0 || end > 512 {
			return "", fmt.Errorf("PE: bad DLL name at RVA 0x%x", rva)
		}
		return strings.ToLower(string(data[o : o+end])), nil
	}

	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	const maxDescriptors = 4096

	// Import directory (index 1): IMAGE_IMPORT_DESCRIPTOR, 20 bytes, Name at +12.
	if len(dirs) > 1 && dirs[1].VirtualAddress != 0 {
		o, ok := off(dirs[1].VirtualAddress)
		if !ok {
			return nil, errors.New("PE: import directory outside the sections")
		}
		for i := 0; ; i++ {
			if i >= maxDescriptors || o+20 > len(data) {
				return nil, errors.New("PE: unterminated import directory")
			}
			d := data[o : o+20]
			if bytes.Equal(d, make([]byte, 20)) {
				break
			}
			n, err := cstr(binary.LittleEndian.Uint32(d[12:]))
			if err != nil {
				return nil, err
			}
			add(n)
			o += 20
		}
	}

	// Delay-load directory (index 13): ImgDelayDescr, 32 bytes, Attributes at
	// +0, DllNameRVA at +4 (a VA in the old format, Attributes bit 0 clear).
	if len(dirs) > 13 && dirs[13].VirtualAddress != 0 {
		o, ok := off(dirs[13].VirtualAddress)
		if !ok {
			return nil, errors.New("PE: delay-load directory outside the sections")
		}
		for i := 0; ; i++ {
			if i >= maxDescriptors || o+32 > len(data) {
				return nil, errors.New("PE: unterminated delay-load directory")
			}
			d := data[o : o+32]
			if bytes.Equal(d, make([]byte, 32)) {
				break
			}
			rva := binary.LittleEndian.Uint32(d[4:])
			if binary.LittleEndian.Uint32(d[0:])&1 == 0 && uint64(rva) >= imageBase {
				rva = uint32(uint64(rva) - imageBase)
			}
			n, err := cstr(rva)
			if err != nil {
				return nil, err
			}
			add(n)
			o += 32
		}
	}
	return out, nil
}

// peMachineMatches checks that a PE image is built for goarch (a DLL of
// another architecture cannot be loaded; say so instead of the loader's
// "not a valid Win32 application").
func peMachineMatches(data []byte, goarch string) error {
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer f.Close()
	want := map[string]uint16{
		"amd64": pe.IMAGE_FILE_MACHINE_AMD64,
		"386":   pe.IMAGE_FILE_MACHINE_I386,
		"arm64": pe.IMAGE_FILE_MACHINE_ARM64,
	}[goarch]
	if want == 0 || f.FileHeader.Machine == want {
		return nil
	}
	return fmt.Errorf("built for machine 0x%x, this process is %s", f.FileHeader.Machine, goarch)
}

// dirCount: the data directories actually present (at most 16).
func dirCount(n uint32) int {
	if n > 16 {
		return 16
	}
	return int(n)
}

// isAPISetName: an API set contract (resolved by the loader from its
// schema, never searched for as a file).
func isAPISetName(n string) bool {
	return strings.HasPrefix(n, "api-ms-") || strings.HasPrefix(n, "ext-ms-")
}
