//go:build linux

package main

// trash_linux.go — "delete" of the local pane on Linux: to the Trash, per
// the freedesktop.org Trash specification 1.0, the layout GIO (Nautilus,
// gio trash), KIO (Dolphin) and the Trash portal share, so the files show in
// the desktop's own Trash and can be restored from there.
//
//   - a file on the same file system as the home Trash goes to
//     $XDG_DATA_HOME/Trash (the user's own, HOST_XDG_DATA_HOME inside the
//     Flatpak, never the sandbox's) — files/ + info/<name>.trashinfo with
//     the absolute Path;
//   - a file on another volume goes to that volume's top directory:
//     $topdir/.Trash/$uid when $topdir/.Trash is a real sticky directory
//     (an administrator's shared trash), else $topdir/.Trash-$uid (created
//     0700, ours, not a link); its Path is then relative to $topdir;
//   - the .trashinfo is created first (O_EXCL: its name reserves the slot),
//     then the element renamed into files/; a failure removes the
//     .trashinfo. Nothing is ever deleted for good: no Trash usable on that
//     volume = no_recycle (the page says it, as for a Windows drive without
//     a Recycle Bin).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// trashDir: a Trash (its files/ and info/ inside), and how its .trashinfo
// names a path (relative to top for a volume's Trash, absolute otherwise).
type trashDir struct {
	dir string
	top string // "" : absolute paths
}

// homeTrash: the user's home Trash.
func homeTrash() string { return filepath.Join(hostDataHome(), "Trash") }

// trashEscape: a path as a .trashinfo Path value (RFC 2396 escaping, '/'
// kept).
func trashEscape(p string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()/", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// trashInfo: the content of a .trashinfo.
func trashInfo(path string, when time.Time) []byte {
	return []byte("[Trash Info]\nPath=" + trashEscape(path) + "\nDeletionDate=" + when.Format("2006-01-02T15:04:05") + "\n")
}

// ensureOwnDir: dir exists (created with mode), is a real directory (not a
// link) owned by this user.
func ensureOwnDir(dir string, mode os.FileMode) error {
	if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s: not a directory of this user", dir)
	}
	return nil
}

// usableTrash: files/ and info/ ready in t.
func usableTrash(t string) error {
	if err := ensureOwnDir(t, 0o700); err != nil {
		return err
	}
	if err := ensureOwnDir(filepath.Join(t, "files"), 0o700); err != nil {
		return err
	}
	return ensureOwnDir(filepath.Join(t, "info"), 0o700)
}

func devOf(p string) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return 0, err
	}
	return st.Dev, nil
}

// trashSameDevice: a and b are on the same file system (the tests fake
// another volume).
var trashSameDevice = func(a, b string) bool {
	da, err1 := devOf(a)
	db, err2 := devOf(b)
	return err1 == nil && err2 == nil && da == db
}

// trashFor: the Trash an element goes to.
func trashFor(p string) (trashDir, error) {
	home := homeTrash()
	if _, err := os.Lstat(p); err != nil {
		return trashDir{}, err
	}
	// The home Trash's file system: the Trash itself, else its nearest
	// existing parent (created on first use).
	probe := home
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	if trashSameDevice(probe, p) {
		if err := os.MkdirAll(filepath.Dir(home), 0o700); err != nil {
			return trashDir{}, fsE("no_recycle")
		}
		if err := usableTrash(home); err != nil {
			return trashDir{}, fsE("no_recycle")
		}
		return trashDir{dir: home}, nil
	}
	// Another volume: its top directory's Trash.
	m, ok := mountOf(readMounts(), p)
	if !ok || m.point == p {
		return trashDir{}, fsE("no_recycle")
	}
	uid := strconv.Itoa(os.Getuid())
	shared := filepath.Join(m.point, ".Trash")
	var st unix.Stat_t
	if unix.Lstat(shared, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&unix.S_ISVTX != 0 {
		t := filepath.Join(shared, uid)
		if usableTrash(t) == nil {
			return trashDir{dir: t, top: m.point}, nil
		}
	}
	t := filepath.Join(m.point, ".Trash-"+uid)
	if err := usableTrash(t); err != nil {
		return trashDir{}, fsE("no_recycle")
	}
	return trashDir{dir: t, top: m.point}, nil
}

// trashPath moves one element to its Trash.
func trashPath(p string, now time.Time) error {
	t, err := trashFor(p)
	if err != nil {
		return err
	}
	if pathUnder(t.dir, p) {
		return fsE("protected") // (the Trash itself, or a folder holding it)
	}
	infoPath := p
	if t.top != "" {
		rel, err := filepath.Rel(t.top, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fsE("no_recycle")
		}
		infoPath = rel
	}
	base := filepath.Base(p)
	for i := 1; i < 10000; i++ {
		name := base
		if i > 1 {
			name = base + "." + strconv.Itoa(i)
		}
		info := filepath.Join(t.dir, "info", name+".trashinfo")
		f, err := os.OpenFile(info, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return fsE("no_recycle")
		}
		_, werr := f.Write(trashInfo(infoPath, now))
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(info)
			return fsE("no_recycle")
		}
		dst := filepath.Join(t.dir, "files", name)
		if err := (linuxSys{}).moveNoReplace(p, dst); err != nil {
			os.Remove(info)
			if os.IsExist(err) {
				continue
			}
			if errors.Is(err, unix.EXDEV) {
				return fsE("no_recycle")
			}
			return err
		}
		return nil
	}
	return fsE("no_recycle")
}
