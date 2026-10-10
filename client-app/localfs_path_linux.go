//go:build linux

package main

// localfs_path_linux.go — the POSIX path rules of localfs.go (its header
// says them for Windows): absolute paths only ("/..."), no "." nor ".."
// element, no NUL nor control character, 1024 UTF-16 units at most (the same
// bound as Windows, well under PATH_MAX); comparisons are exact (Linux file
// systems are case-sensitive). The names this app creates stay the portable
// ones of sanitizeLocalName (valid on Windows too).

import (
	"path/filepath"
	"strings"
)

// localPathOK: an absolute local path the page may name, cleaned; an
// invalid_path refusal otherwise.
func localPathOK(p string) (string, error) {
	if p == "" || len(p) > 4*localMaxPathUnits || utf16Len(p) > localMaxPathUnits {
		return "", fsE("invalid_path")
	}
	if p[0] != '/' {
		return "", fsE("invalid_path")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == 0xFFFD {
			return "", fsE("invalid_path")
		}
	}
	for _, c := range strings.Split(p[1:], "/") {
		if c == "." || c == ".." {
			return "", fsE("invalid_path")
		}
		if len(c) > 255 {
			return "", fsE("invalid_path")
		}
	}
	return filepath.Clean(p), nil
}

// isDriveRoot: the file system's root ("/"); the mount points are kept by
// keepRoots (localfs_linux.go).
func isDriveRoot(p string) bool { return p == "/" }

// localExistingNameOK: the name of an element already in a folder, as the
// page names it: one element (no '/', not "." nor ".."), no control
// character, 255 bytes at most (NAME_MAX). Names Windows refuses (':', '?',
// a trailing dot...) are fine here: they exist on Linux disks.
func localExistingNameOK(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 255 {
		return false
	}
	for _, r := range n {
		if r == '/' || r < 0x20 || r == 0x7f || r == 0xFFFD {
			return false
		}
	}
	return true
}

// pathUnder: p is root or inside it (cleaned paths, case-sensitive).
func pathUnder(p, root string) bool {
	if p == "" || root == "" {
		return false
	}
	lp, lr := filepath.Clean(p), filepath.Clean(root)
	if lp == lr {
		return true
	}
	if !strings.HasSuffix(lr, "/") {
		lr += "/"
	}
	return strings.HasPrefix(lp, lr)
}

// samePath: the same path for the file system (exact).
func samePath(a, b string) bool { return a == b }
