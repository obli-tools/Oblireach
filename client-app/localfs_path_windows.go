//go:build windows

package main

// localfs_path_windows.go — the Windows path rules of localfs.go (see its
// header): drive paths only, case-insensitive comparisons.

import (
	"path/filepath"
	"strings"
)

// localPathOK: an absolute local path the page may name, cleaned; an
// invalid_path refusal otherwise (see localfs.go's comment).
func localPathOK(p string) (string, error) {
	if p == "" || len(p) > 4*localMaxPathUnits || utf16Len(p) > localMaxPathUnits {
		return "", fsE("invalid_path")
	}
	p = strings.ReplaceAll(p, "/", `\`)
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `\??\`) {
		return "", fsE("invalid_path")
	}
	if len(p) < 3 || !isDriveLetter(p[0]) || p[1] != ':' || p[2] != '\\' {
		return "", fsE("invalid_path")
	}
	for i, r := range p {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`*?"<>|`, r) || (r == ':' && i != 1) {
			return "", fsE("invalid_path")
		}
	}
	for _, c := range strings.Split(p[3:], `\`) {
		if c == "" {
			continue
		}
		if c == "." || c == ".." || strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ") || reservedDeviceName(c) {
			return "", fsE("invalid_path")
		}
	}
	return filepath.Clean(p), nil
}

// isDriveRoot: "C:\".
func isDriveRoot(p string) bool {
	return len(p) == 3 && isDriveLetter(p[0]) && p[1] == ':' && p[2] == '\\'
}

// localExistingNameOK: the name of an element already in a folder, as the
// page names it (a selection sent): one element Windows accepts
// (portableNameOK, localfs.go).
func localExistingNameOK(n string) bool { return portableNameOK(n) }

// pathUnder: p is root or inside it (cleaned paths, case-insensitive).
func pathUnder(p, root string) bool {
	if p == "" || root == "" {
		return false
	}
	lp, lr := strings.ToLower(filepath.Clean(p)), strings.ToLower(filepath.Clean(root))
	if lp == lr {
		return true
	}
	if !strings.HasSuffix(lr, `\`) {
		lr += `\`
	}
	return strings.HasPrefix(lp, lr)
}

// samePath: the same path for the file system (case-insensitive).
func samePath(a, b string) bool { return strings.EqualFold(a, b) }
