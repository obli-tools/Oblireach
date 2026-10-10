//go:build linux

package main

// localfs_linux.go — the Linux side of localfs.go (localSys: the disk and the
// desktop) and the main window's local file system with this system's
// protected locations. The rules and the bindings (localfs.go,
// localfs_bridge.go) are platform-neutral; the path rules are POSIX here
// (localfs_path_linux.go).
//
//   - listing: os.ReadDir + lstat; a dot file is hidden, a device / FIFO /
//     socket is "system", read-only = not writable by this user (access(2));
//     a link is listed as what it points to, marked as a link;
//   - roots: the home folder, the XDG user folders (Desktop, Documents,
//     Downloads, ~/.config/user-dirs.dirs), "/" outside the Flatpak (inside
//     it "/" is the sandbox's own), and the mounted volumes under
//     /run/media/$USER, /media and /mnt (/proc/self/mountinfo, each probed
//     with statfs within 3 s: a dead network mount never hangs the list);
//   - deletion: to the Trash, per the freedesktop.org Trash specification
//     (trash_linux.go), the same layout GIO/Dolphin/Nautilus use, never a
//     permanent delete;
//   - reveal: the OpenURI portal's OpenDirectory (the file manager on the
//     element's folder, the element selected), org.freedesktop.FileManager1
//     outside a sandbox when no portal answers;
//   - Mark-of-the-Web: no Linux equivalent is enforced by anything; the
//     files are written 0644 without an execute bit (localfs.go), which is
//     what keeps a downloaded script from running by a double click.

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// GetDriveTypeW's values, which localfs.go speaks (localDriveFixed = 3).
const (
	driveRemovable = 2
	driveRemote    = 4
	driveCDROM     = 5
	driveRAMDisk   = 6
)

type linuxSys struct{}

var _ localSys = linuxSys{}

// entryAt: a localEntry of path (name given), from its lstat.
func entryAt(path, name string) (localEntry, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return localEntry{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	e := localEntry{Name: name, Hidden: strings.HasPrefix(name, ".")}
	mode := st.Mode & unix.S_IFMT
	if mode == unix.S_IFLNK {
		e.Link = true
		var t unix.Stat_t
		if unix.Stat(path, &t) == nil {
			st = t
			mode = st.Mode & unix.S_IFMT
		}
	}
	switch mode {
	case unix.S_IFDIR:
		e.IsDir = true
	case unix.S_IFREG, unix.S_IFLNK:
		e.Size = st.Size
	default:
		e.System = true // a device, a FIFO, a socket
	}
	e.Modified = time.Unix(st.Mtim.Sec, st.Mtim.Nsec)
	e.ReadOnly = unix.Access(path, unix.W_OK) != nil
	return e, nil
}

func (linuxSys) readDir(dir string) ([]localEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]localEntry, 0, len(des))
	for _, d := range des {
		e, err := entryAt(filepath.Join(dir, d.Name()), d.Name())
		if err != nil {
			continue // (gone meanwhile)
		}
		out = append(out, e)
	}
	return out, nil
}

func (linuxSys) lstat(path string) (localEntry, error) {
	return entryAt(path, filepath.Base(path))
}

func (linuxSys) finalPath(path string) (string, error) { return filepath.EvalSymlinks(path) }

// ── Mounts ─────────────────────────────────────────────────────────────────

// mountEntry: one line of /proc/self/mountinfo.
type mountEntry struct {
	point  string
	fstype string
	source string
}

// mountInfoPath: the mount table (tests: a fixture).
var mountInfoPath = "/proc/self/mountinfo"

// unescapeMount: the octal escapes of mountinfo (\040 a space...).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseMountInfo: the mounts of a mountinfo text, in its order.
func parseMountInfo(text string) []mountEntry {
	var out []mountEntry
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := -1
		for i, x := range f {
			if x == "-" {
				sep = i
				break
			}
		}
		if len(f) < 5 || sep < 6 || sep+2 >= len(f) {
			continue
		}
		out = append(out, mountEntry{point: unescapeMount(f[4]), fstype: f[sep+1], source: unescapeMount(f[sep+2])})
	}
	return out
}

func readMounts() []mountEntry {
	b, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return nil
	}
	return parseMountInfo(string(b))
}

// mountOf: the mount holding path (the longest mount point it is under; the
// last such mount wins, as the kernel stacks them).
func mountOf(mounts []mountEntry, path string) (mountEntry, bool) {
	best, found := mountEntry{}, false
	for _, m := range mounts {
		if pathUnder(path, m.point) && (!found || len(m.point) >= len(best.point)) {
			best, found = m, true
		}
	}
	return best, found
}

var (
	remoteFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "9p": true, "ceph": true,
		"glusterfs": true, "afs": true, "davfs": true, "fuse.sshfs": true, "fuse.rclone": true, "fuse.davfs2": true,
		"fuse.gvfsd-fuse": true, "fuse.s3fs": true, "virtiofs": true}
	pseudoFS = map[string]bool{"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "cgroup": true, "cgroup2": true,
		"securityfs": true, "pstore": true, "bpf": true, "debugfs": true, "tracefs": true, "configfs": true, "fusectl": true,
		"mqueue": true, "hugetlbfs": true, "autofs": true, "binfmt_misc": true, "efivarfs": true, "rpc_pipefs": true,
		"nsfs": true, "overlay": true, "squashfs": true, "fuse.portal": true, "fuse.xdg-document-portal": true}
)

// fsDriveType: the GetDriveTypeW-like class of a file system type.
func fsDriveType(m mountEntry) uint32 {
	switch {
	case remoteFS[m.fstype] || strings.HasPrefix(m.source, "//"):
		return driveRemote
	case m.fstype == "iso9660" || m.fstype == "udf":
		return driveCDROM
	case m.fstype == "tmpfs" || m.fstype == "ramfs":
		return driveRAMDisk
	}
	return localDriveFixed
}

// driveType: "fixed" (localDriveFixed) for a local disk file system, the
// only kind localfs.go sends to the Trash (a USB stick included: it has its
// own .Trash-$UID); network, optical and RAM file systems are not.
func (linuxSys) driveType(path string) uint32 {
	m, ok := mountOf(readMounts(), path)
	if !ok {
		return 0
	}
	return fsDriveType(m)
}

// removableRoots: the folders removable volumes are mounted under.
func removableRoots() []string {
	user := os.Getenv("USER")
	out := []string{}
	if user != "" {
		out = append(out, filepath.Join("/run/media", user))
	}
	return append(out, "/media", "/mnt")
}

// volumeMounts: the mounted volumes the local pane offers (under
// removableRoots), deduplicated, sorted.
func volumeMounts(mounts []mountEntry) []mountEntry {
	seen := map[string]bool{}
	var out []mountEntry
	for _, m := range mounts {
		if pseudoFS[m.fstype] || seen[m.point] {
			continue
		}
		for _, r := range removableRoots() {
			if m.point != r && pathUnder(m.point, r) || (m.point == "/mnt" && r == "/mnt") {
				seen[m.point] = true
				out = append(out, m)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].point < out[j].point })
	return out
}

func driveKindOf(m mountEntry) string {
	switch fsDriveType(m) {
	case driveRemote:
		return "network"
	case driveCDROM:
		return "cdrom"
	case driveRAMDisk:
		return "ram"
	}
	for _, r := range removableRoots()[:len(removableRoots())-1] { // /run/media/$USER, /media
		if pathUnder(m.point, r) {
			return "removable"
		}
	}
	return "fixed"
}

// statfsWithin: the volume answers statfs within d.
func statfsWithin(path string, d time.Duration) bool {
	ch := make(chan bool, 1)
	go func() {
		var s unix.Statfs_t
		ch <- unix.Statfs(path, &s) == nil
	}()
	select {
	case ok := <-ch:
		return ok
	case <-time.After(d):
		return false
	}
}

// ── XDG user folders ───────────────────────────────────────────────────────

// hostConfigHome: the user's own ~/.config (inside the Flatpak
// XDG_CONFIG_HOME is the app's: HOST_XDG_CONFIG_HOME, else ~/.config).
func hostConfigHome() string {
	if d := os.Getenv("HOST_XDG_CONFIG_HOME"); filepath.IsAbs(d) {
		return d
	}
	if !inFlatpak() {
		if d := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(d) {
			return d
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// hostDataHome: the user's own ~/.local/share (same rule).
func hostDataHome() string {
	if d := os.Getenv("HOST_XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d
	}
	if !inFlatpak() {
		if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
			return d
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// parseUserDirs: user-dirs.dirs' XDG_<NAME>_DIR values ($HOME expanded;
// only absolute or $HOME-relative values, as xdg-user-dirs writes them).
func parseUserDirs(text, home string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(k, "XDG_") || !strings.HasSuffix(k, "_DIR") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch {
		case strings.HasPrefix(v, "$HOME/"):
			v = filepath.Join(home, v[len("$HOME/"):])
		case v == "$HOME":
			v = home
		case !filepath.IsAbs(v):
			continue
		}
		out[strings.TrimSuffix(strings.TrimPrefix(k, "XDG_"), "_DIR")] = filepath.Clean(v)
	}
	return out
}

// userDir: an XDG user folder ("DESKTOP", "DOCUMENTS", "DOWNLOAD"), "" when
// it is not set or is the home folder itself (xdg-user-dirs' "disabled").
func userDir(name string) string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	b, _ := os.ReadFile(filepath.Join(hostConfigHome(), "user-dirs.dirs"))
	dirs := parseUserDirs(string(b), home)
	p, ok := dirs[name]
	if !ok {
		p = filepath.Join(home, map[string]string{"DESKTOP": "Desktop", "DOCUMENTS": "Documents", "DOWNLOAD": "Downloads"}[name])
	}
	if p == home || p == "" {
		return ""
	}
	return p
}

func (linuxSys) roots() localRoots {
	var r localRoots
	r.Computer, _ = os.Hostname()
	r.Home, _ = os.UserHomeDir()
	r.Places = []localPlace{}
	if r.Home != "" {
		r.Places = append(r.Places, localPlace{ID: "home", Path: r.Home})
	}
	for _, kf := range []struct{ id, xdg string }{{"desktop", "DESKTOP"}, {"documents", "DOCUMENTS"}, {"downloads", "DOWNLOAD"}} {
		if p := userDir(kf.xdg); p != "" {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				r.Places = append(r.Places, localPlace{ID: kf.id, Path: p})
			}
		}
	}
	r.Drives = []localDrive{}
	if !inFlatpak() {
		r.Drives = append(r.Drives, localDrive{Path: "/", Kind: "fixed", Label: r.Computer})
	}
	vols := volumeMounts(readMounts())
	ok := make([]bool, len(vols))
	var wg sync.WaitGroup
	for i, m := range vols {
		wg.Add(1)
		go func(i int, p string) { defer wg.Done(); ok[i] = statfsWithin(p, 3*time.Second) }(i, m.point)
	}
	wg.Wait()
	for i, m := range vols {
		if !ok[i] {
			continue
		}
		r.Drives = append(r.Drives, localDrive{Path: m.point, Kind: driveKindOf(m), Label: filepath.Base(m.point)})
	}
	return r
}

// ── Attributes, moves ──────────────────────────────────────────────────────

// setHidden: a dot name is what hides a file here (the app's temporary
// files already have one): nothing to set.
func (linuxSys) setHidden(string) {}

// markOfTheWeb: nothing enforces one on Linux (see the file's header).
func (linuxSys) markOfTheWeb(string) error { return nil }

// moveNoReplace: rename, failing when dst exists (renameat2
// RENAME_NOREPLACE; a file system without it: checked, then renamed).
func (linuxSys) moveNoReplace(src, dst string) error {
	err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	if err == unix.EINVAL || err == unix.ENOSYS || err == unix.ENOTSUP {
		if _, lerr := os.Lstat(dst); lerr == nil {
			return &os.LinkError{Op: "rename", Old: src, New: dst, Err: unix.EEXIST}
		}
		return os.Rename(src, dst)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}

func (linuxSys) moveReplace(src, dst string) error { return os.Rename(src, dst) }

func (linuxSys) recycle(paths []string) error {
	for _, p := range paths {
		if err := trashPath(p, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

// reveal: the file manager on the element's folder, the element selected.
func (linuxSys) reveal(path string) error {
	if err := openURIDirectory(path); err != nil {
		if errors.Is(err, errPortalAbsent) && !inFlatpak() {
			return fileManagerShow(path)
		}
		return fsE("io")
	}
	return nil
}

// ── The main window's local file system ────────────────────────────────────

// linuxProtected: the locations of this user's session where a file written
// runs code or changes the session, and the system's own folders. Nothing
// is written, renamed nor trashed there from the local pane. The whole of
// ~/.config and ~/.local (autostart, systemd user units, environment.d,
// D-Bus activation, Flatpak overrides, desktop entries, file manager
// actions, KWin / GNOME Shell extensions, every program's init files...),
// the shell profiles, ~/bin, ~/.ssh, ~/.gnupg and the other apps' Flatpak
// data: listing them stays possible, writing does not.
func linuxProtected(home, cfg, data string) []string {
	var out []string
	if home != "" {
		for _, n := range []string{".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile", ".zshrc", ".zprofile",
			".zshenv", ".zlogin", ".zlogout", ".xprofile", ".xinitrc", ".xsession", ".xsessionrc", ".pam_environment",
			".ssh", ".gnupg", ".config", ".local", "bin", ".var/app"} {
			out = append(out, filepath.Join(home, n))
		}
	}
	out = append(out, cfg, data)
	// The system: not /var nor /run as a whole (Fedora Atomic / Bazzite keep
	// the homes in /var/home, removable volumes are under /run/media).
	return append(out, "/etc", "/usr", "/boot", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/sys", "/proc", "/dev",
		"/app", "/sysroot", "/ostree", "/var/lib", "/var/log", "/var/cache", "/var/spool", "/var/opt", "/var/usrlocal",
		"/var/roothome", "/var/srv", "/var/lib/flatpak", "/run/user", "/run/lock", "/run/systemd", "/run/dbus",
		"/run/host", "/run/flatpak")
}

// newLocalFS: the main window's local file system (its working copies under
// $XDG_DATA_HOME/oblireach/edit).
func newLocalFS(p *Proxy) *localFS {
	editRoot := ""
	if d := platformPaths.localDataDir(); d != "" {
		editRoot = filepath.Join(d, "edit")
	}
	l := newLocalFSWith(linuxSys{}, editRoot)
	add := func(list *[]string, ps ...string) {
		for _, x := range ps {
			if x != "" && filepath.IsAbs(x) {
				*list = append(*list, filepath.Clean(x))
			}
		}
	}
	home, _ := os.UserHomeDir()
	cfg, data := hostConfigHome(), hostDataHome()
	add(&l.protectW, linuxProtected(home, cfg, data)...)
	// The app's own data: never read (its session cookies, the vault, the
	// web view's profile, the working copies through the edit bindings
	// only).
	own := []string{platformPaths.configDir(), platformPaths.localDataDir()}
	if c, err := os.UserCacheDir(); err == nil {
		own = append(own, filepath.Join(c, linuxAppDirName))
	}
	if home != "" {
		own = append(own, filepath.Join(home, ".var", "app", ssAppID))
	}
	if exe, err := os.Executable(); err == nil {
		add(&l.protectW, filepath.Dir(exe))
	}
	add(&l.protectW, own...)
	add(&l.protectR, own...)
	add(&l.keepRoots, home, filepath.Dir(home), "/", "/home", "/var/home", "/run/media", "/media", "/mnt",
		userDir("DESKTOP"), userDir("DOCUMENTS"), userDir("DOWNLOAD"), cfg, data)
	for _, r := range removableRoots() {
		add(&l.keepRoots, r)
	}
	for _, m := range volumeMounts(readMounts()) {
		add(&l.keepRoots, m.point)
	}
	if p != nil {
		l.settings = p.filesSettings
		l.setSettings = p.setFilesSettings
		l.edits.origin = p.profileOrigin
	}
	if editRoot != "" {
		go sweepEditRoot(editRoot, time.Now(), editSweepAge)
	}
	return l
}
