package main

// localconn_explorer.go — the backend of the file explorer (explorer_viewer.go)
// for a LOCAL SSH connection (connections_page.go, the vault's ssh entries):
// the explorer speaks one vocabulary whatever the device (an Obliance
// agent's files.go commands: list_directory, create_directory, rename_file,
// delete_file, download_file, upload_file, with their payloads and results),
// and for a local connection the page sends those same commands to the app
// (reachSftpCmd, sftp_bridge.go), which runs them here on the
// connection's own SFTP session (sftp_session.go) instead of the device's
// server socket. One explorer, two transports: no second view to maintain.
//
// The results have the agent's shapes (explorer_viewer.go explorerItems,
// explorerDownload, explorerUpload, transfers.go, edit_page.go read them):
//   - list_directory {path} -> {path, files: [{name, path, isDir, size,
//     modified (RFC 3339), mode (os.FileMode.String(): "L..." for a link)}]};
//     "" lists the session's home (the server's ".");
//   - download_file {path} -> {data: base64} (explorerMaxBytes at most, as
//     the explorer itself checks);
//   - upload_file {path, data, overwrite}: a file already there without
//     overwrite fails with "already exists" (the explorer then asks);
//   - delete_file {path, recursive}: a folder with contents only recursive.
// Failures are plain sentences (the explorer quotes them), never a secret.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// explorerMaxBytes: the explorer's own limit (EXPLORER_MAX_BYTES).
const explorerMaxBytes = 100 * 1024 * 1024

// explorerFS: what the commands need of an SFTP session (sftpSession below;
// a fake in the tests).
type explorerFS interface {
	RealPath(p string) (string, error)
	ReadDir(dir string) ([]os.FileInfo, error)
	Lstat(p string) (os.FileInfo, error)
	Mkdir(p string) error
	Rename(oldPath, newPath string) error
	Remove(p string) error
	RemoveDirectory(p string) error
	Open(p string) (io.ReadCloser, error)
	Create(p string) (io.WriteCloser, error)
}

var (
	errExplorerBadCmd   = errors.New("unknown command")
	errExplorerBadPath  = errors.New("invalid path")
	errExplorerExists   = errors.New("already exists")
	errExplorerTooLarge = errors.New("file too large")
	errExplorerNotEmpty = errors.New("folder not empty")
)

type explorerFile struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Mode     string `json:"mode"`
}

type explorerListing struct {
	Path  string         `json:"path"`
	Files []explorerFile `json:"files"`
}

// explorerCleanPath: an absolute POSIX path of the remote side, or "" (the
// home). The page builds paths from what a listing returned (explorerJoin):
// a NUL or a relative path is refused rather than guessed at.
func explorerCleanPath(p string, allowEmpty bool) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", errExplorerBadPath
	}
	if p == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errExplorerBadPath
	}
	if !strings.HasPrefix(p, "/") {
		return "", errExplorerBadPath
	}
	c := path.Clean(p)
	return c, nil
}

// localExplorerCmd runs one explorer command on fs. payload is the
// command's JSON payload as the page sent it.
func localExplorerCmd(fs explorerFS, typ string, payload []byte) (interface{}, error) {
	var in struct {
		Path      string `json:"path"`
		OldPath   string `json:"oldPath"`
		NewPath   string `json:"newPath"`
		Recursive bool   `json:"recursive"`
		Overwrite bool   `json:"overwrite"`
		Data      string `json:"data"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, errExplorerBadPath
		}
	}
	switch typ {
	case "list_directory":
		p, err := explorerCleanPath(in.Path, true)
		if err != nil {
			return nil, err
		}
		if p == "" {
			if p, err = fs.RealPath("."); err != nil {
				return nil, err
			}
		}
		infos, err := fs.ReadDir(p)
		if err != nil {
			return nil, err
		}
		out := explorerListing{Path: p, Files: make([]explorerFile, 0, len(infos))}
		for _, fi := range infos {
			if fi == nil || fi.Name() == "" || fi.Name() == "." || fi.Name() == ".." {
				continue
			}
			out.Files = append(out.Files, explorerFile{
				Name: fi.Name(), Path: explorerJoinPath(p, fi.Name()), IsDir: fi.IsDir(), Size: fi.Size(),
				Modified: fi.ModTime().UTC().Format(time.RFC3339), Mode: fi.Mode().String(),
			})
		}
		sort.SliceStable(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })
		return out, nil
	case "create_directory":
		p, err := explorerCleanPath(in.Path, false)
		if err != nil || p == "/" {
			return nil, errExplorerBadPath
		}
		if _, err := fs.Lstat(p); err == nil {
			return nil, errExplorerExists
		}
		return struct{}{}, fs.Mkdir(p)
	case "rename_file":
		o, err := explorerCleanPath(in.OldPath, false)
		if err != nil || o == "/" {
			return nil, errExplorerBadPath
		}
		n, err := explorerCleanPath(in.NewPath, false)
		if err != nil || n == "/" {
			return nil, errExplorerBadPath
		}
		if o == n {
			return struct{}{}, nil
		}
		if _, err := fs.Lstat(n); err == nil {
			return nil, errExplorerExists
		}
		return struct{}{}, fs.Rename(o, n)
	case "delete_file":
		p, err := explorerCleanPath(in.Path, false)
		if err != nil || p == "/" {
			return nil, errExplorerBadPath
		}
		return struct{}{}, explorerRemove(fs, p, in.Recursive, 0)
	case "download_file":
		p, err := explorerCleanPath(in.Path, false)
		if err != nil {
			return nil, err
		}
		fi, err := fs.Lstat(p)
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			return nil, errExplorerBadPath
		}
		if fi.Size() > explorerMaxBytes {
			return nil, errExplorerTooLarge
		}
		rc, err := fs.Open(p)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, explorerMaxBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > explorerMaxBytes {
			return nil, errExplorerTooLarge
		}
		return struct {
			Data string `json:"data"`
		}{base64.StdEncoding.EncodeToString(data)}, nil
	case "upload_file":
		p, err := explorerCleanPath(in.Path, false)
		if err != nil || p == "/" {
			return nil, errExplorerBadPath
		}
		data, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			return nil, errExplorerBadPath
		}
		if len(data) > explorerMaxBytes {
			return nil, errExplorerTooLarge
		}
		if fi, err := fs.Lstat(p); err == nil {
			if fi.IsDir() || !in.Overwrite {
				return nil, errExplorerExists
			}
		}
		// (its folders, like the agent: a folder's files sent one by one)
		if err := explorerMkdirAll(fs, path.Dir(p), 0); err != nil {
			return nil, err
		}
		wc, err := explorerCreate(fs, p, int64(len(data)))
		if err != nil {
			return nil, err
		}
		if _, err := wc.Write(data); err != nil {
			wc.Close()
			return nil, err
		}
		return struct{}{}, wc.Close()
	}
	return nil, errExplorerBadCmd
}

// explorerCreate: a new file of size bytes (SCP needs its size first:
// scp.go; size < 0: unknown, SFTP only).
func explorerCreate(fs explorerFS, p string, size int64) (io.WriteCloser, error) {
	if sf, ok := fs.(explorerSizedFS); ok && size >= 0 {
		return sf.CreateSized(p, size)
	}
	return fs.Create(p)
}

// explorerJoinPath: dir + "/" + name on the remote (POSIX) side.
func explorerJoinPath(dir, name string) string {
	if dir == "" || dir == "/" {
		return "/" + name
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

// explorerRemove: a file or a link (never followed), or a folder (its
// contents first, only when recursive). depth bounds a pathological tree.
func explorerRemove(fs explorerFS, p string, recursive bool, depth int) error {
	if depth > 64 {
		return errExplorerBadPath
	}
	fi, err := fs.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fs.Remove(p)
	}
	infos, err := fs.ReadDir(p)
	if err != nil {
		return err
	}
	kids := 0
	for _, k := range infos {
		if k != nil && k.Name() != "." && k.Name() != ".." {
			kids++
		}
	}
	if kids > 0 && !recursive {
		return errExplorerNotEmpty
	}
	for _, k := range infos {
		if k == nil || k.Name() == "." || k.Name() == ".." {
			continue
		}
		if err := explorerRemove(fs, explorerJoinPath(p, k.Name()), true, depth+1); err != nil {
			return err
		}
	}
	return fs.RemoveDirectory(p)
}

// explorerCmdErrText: a failure in words for the page (quoted by the
// explorer), bounded, never more than what the SFTP server said.
func explorerCmdErrText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if errors.Is(err, os.ErrNotExist) {
		s = "not found"
	} else if errors.Is(err, os.ErrPermission) {
		s = "permission denied"
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// ── sftpSession as an explorerFS ────────────────────────────────────────────

type sftpExplorerFS struct{ s *sftpSession }

func (f sftpExplorerFS) RealPath(p string) (string, error)         { return f.s.sftp.RealPath(p) }
func (f sftpExplorerFS) ReadDir(dir string) ([]os.FileInfo, error) { return f.s.sftp.ReadDir(dir) }
func (f sftpExplorerFS) Lstat(p string) (os.FileInfo, error)       { return f.s.sftp.Lstat(p) }
func (f sftpExplorerFS) Mkdir(p string) error                      { return f.s.sftp.Mkdir(p) }
func (f sftpExplorerFS) Rename(o, n string) error                  { return f.s.sftp.Rename(o, n) }
func (f sftpExplorerFS) Remove(p string) error                     { return f.s.sftp.Remove(p) }
func (f sftpExplorerFS) RemoveDirectory(p string) error            { return f.s.sftp.RemoveDirectory(p) }
func (f sftpExplorerFS) Open(p string) (io.ReadCloser, error)      { return f.s.sftp.Open(p) }
func (f sftpExplorerFS) Create(p string) (io.WriteCloser, error)   { return f.s.sftp.Create(p) }

// sftpRunExplorerCmd: localExplorerCmd on a live session, its result as
// the page reads it: {ok:true, result} or {ok:false, error}.
func sftpRunExplorerCmd(s *sftpSession, typ string, payload []byte) map[string]interface{} {
	if s == nil || s.isClosed() {
		return map[string]interface{}{"ok": false, "error": errSFTPGone.Error(), "gone": true}
	}
	fs := s.explorerFS()
	// A download saved by the app (sftp_save.go), a transfer in chunks
	// (sftp_stream.go), else one of the explorer's commands.
	res, handled, err := saveCmd(fs, s.streamsOf(), typ, payload)
	if !handled {
		res, handled, err = streamCmd(fs, s.streamsOf(), typ, payload)
	}
	if !handled {
		res, err = localExplorerCmd(fs, typ, payload)
	}
	if err != nil {
		out := map[string]interface{}{"ok": false, "error": explorerCmdErrText(err)}
		// (SCP's limits, said by the page in its language)
		if errors.Is(err, errScpNoListing) {
			out["code"] = "scp_no_listing"
		} else if errors.Is(err, errScpMissing) {
			out["code"] = "scp_missing"
		}
		if s.isClosed() {
			out["gone"] = true
		}
		return out
	}
	return map[string]interface{}{"ok": true, "result": res}
}
