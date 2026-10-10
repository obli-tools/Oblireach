package main

// localfs_bridge.go — the bindings of localfs.go (moved from
// localfs_windows.go unchanged): the main window's bindings (attachLocalFS),
// a session window's share of them (attachSharedLocalFS: the file bindings
// of the local pane of its Files tabs) and what reaches the page
// (__reachLocalFs, __reachLocalEdit), behind the top-level local UI
// document check. The disk and the shell are the platform's localSys
// (localfs_windows.go winSys).

import (
	"encoding/json"
	"errors"
	"sync/atomic"
)

// recycleOwner: the main window, the owner of the platform's own dialogs
// of a move to the Recycle Bin (attachLocalFS; localfs_windows.go recycle).
var recycleOwner atomic.Uintptr

// attachLocalFS gives the main window's bridge the local file system (before
// bind): its answers and the working copies' events reach the page, the
// working copies are watched while the window lives.
func (b *nativeBridge) attachLocalFS(l *localFS) {
	b.localfs = l
	recycleOwner.Store(b.hwnd)
	l.push = b.localFsAnswer
	l.edits.push = func(evt editEvent) {
		data, err := json.Marshal(evt)
		if err != nil {
			return
		}
		js := string(data)
		b.w.Dispatch(func() { b.evalLocalEdit(js) })
	}
	l.pickEditor = func(done func(path string, ok bool)) {
		b.w.Dispatch(func() {
			path, ok := platformDialogs.pickProgram(b.hwnd, tr("localfs.pickEditorTitle"), tr("localfs.pickEditorFilter"))
			go done(path, ok)
		})
	}
	go l.edits.watch(b.done)
}

// attachSharedLocalFS gives a session window's bridge the main window's
// local file system (before bind), for the local pane of its Files tabs:
// the file bindings only (localFsWindowNames), their answers to this window
// (fsAsync). The editing round trip, the Files settings written and the
// editor picked stay the main window's (fsAsyncMain / fsSyncMain).
func (b *nativeBridge) attachSharedLocalFS(l *localFS) {
	b.localfs = l
	b.localfsShared = true
}

// localFsAnswer: an asynchronous answer to this window's page.
func (b *nativeBridge) localFsAnswer(reqID string, ok bool, payload []byte) {
	js := string(payload)
	b.w.Dispatch(func() { b.evalLocalFs(reqID, ok, js) })
}

// localUIGuard: the top-level local UI document (/) only, never a page of a
// server nor a frame.
func (b *nativeBridge) localUIGuard() string {
	origin, _ := json.Marshal(b.origin)
	return "if (location.origin !== " + string(origin) + " || location.pathname !== '/' || window.top !== window) return; "
}

func (b *nativeBridge) evalLocalFs(reqID string, ok bool, payloadJSON string) {
	if b.closed.Load() {
		return
	}
	id, _ := json.Marshal(reqID)
	okJS := "false"
	if ok {
		okJS = "true"
	}
	b.w.Eval("(function () { " + b.localUIGuard() +
		"var f = window.__reachLocalFs; if (typeof f === 'function') f(" + string(id) + ", " + okJS + ", " + payloadJSON + "); })();")
}

func (b *nativeBridge) evalLocalEdit(evtJSON string) {
	if b.closed.Load() {
		return
	}
	b.w.Eval("(function () { " + b.localUIGuard() +
		"var f = window.__reachLocalEdit; if (typeof f === 'function') f(" + evtJSON + "); })();")
}

// bindLocalFS: the file bindings (bind): all of them in the main window, the
// local pane's in a session window (localFsWindowNames).
func (b *nativeBridge) bindLocalFS() {
	if b.localfsShared {
		for name, f := range map[string]interface{}{
			"reachFsRoots": b.fsRoots, "reachFsList": b.fsList, "reachFsWalk": b.fsWalk, "reachFsOpenRead": b.fsOpenRead,
			"reachFsRead": b.fsRead, "reachFsClose": b.fsClose, "reachFsWriteBegin": b.fsWriteBegin, "reachFsWrite": b.fsWrite,
			"reachFsWriteEnd": b.fsWriteEnd, "reachFsWriteAbort": b.fsWriteAbort, "reachFsMkdir": b.fsMkdir, "reachFsRename": b.fsRename,
			"reachFsTrash": b.fsTrash, "reachFsReveal": b.fsReveal,
			"reachFsWalkLarge": b.fsWalkLarge, "reachFsOpenReadLarge": b.fsOpenReadLarge,
			"reachFilesSettingsGet": b.filesSettingsGet, // (hidden files shown or not: Settings > Files, read only)
		} {
			_ = b.w.Bind(name, f)
		}
		return
	}
	for name, f := range map[string]interface{}{
		"reachFsRoots": b.fsRoots, "reachFsList": b.fsList, "reachFsWalk": b.fsWalk, "reachFsOpenRead": b.fsOpenRead,
		"reachFsRead": b.fsRead, "reachFsClose": b.fsClose, "reachFsWriteBegin": b.fsWriteBegin, "reachFsWrite": b.fsWrite,
		"reachFsWriteEnd": b.fsWriteEnd, "reachFsWriteAbort": b.fsWriteAbort, "reachFsMkdir": b.fsMkdir, "reachFsRename": b.fsRename,
		"reachFsTrash": b.fsTrash, "reachFsReveal": b.fsReveal,
		"reachFsWalkLarge": b.fsWalkLarge, "reachFsOpenReadLarge": b.fsOpenReadLarge, // (a streamed transfer: localfs.go walkLarge)
		"reachFilesSettingsGet": b.filesSettingsGet, "reachFilesSettingsSet": b.filesSettingsSet, "reachFilesPickEditor": b.filesPickEditor,
		"reachEditBegin": b.editBegin, "reachEditReload": b.editReload, "reachEditLaunch": b.editLaunch, "reachEditSnapshot": b.editSnapshot,
		"reachEditSynced": b.editSynced, "reachEditStop": b.editStop, "reachEditList": b.editList, "reachEditRemote": b.editRemote,
	} {
		_ = b.w.Bind(name, f)
	}
}

// localFsNames: the file bindings' names (tests).
var localFsNames = []string{"reachFsRoots", "reachFsList", "reachFsWalk", "reachFsOpenRead", "reachFsRead", "reachFsClose", "reachFsWriteBegin",
	"reachFsWrite", "reachFsWriteEnd", "reachFsWriteAbort", "reachFsMkdir", "reachFsRename", "reachFsTrash", "reachFsReveal",
	"reachFilesSettingsGet", "reachFilesSettingsSet", "reachFilesPickEditor", "reachEditBegin", "reachEditReload", "reachEditLaunch",
	"reachEditSnapshot", "reachEditSynced", "reachEditStop", "reachEditList", "reachEditRemote", "reachFsWalkLarge", "reachFsOpenReadLarge"}

// localFsWindowNames: those a session window has (attachSharedLocalFS; tests).
var localFsWindowNames = []string{"reachFsRoots", "reachFsList", "reachFsWalk", "reachFsOpenRead", "reachFsRead", "reachFsClose", "reachFsWriteBegin",
	"reachFsWrite", "reachFsWriteEnd", "reachFsWriteAbort", "reachFsMkdir", "reachFsRename", "reachFsTrash", "reachFsReveal",
	"reachFsWalkLarge", "reachFsOpenReadLarge", "reachFilesSettingsGet"}

// fsAsync: the checks of every asynchronous call (the token, the request id,
// the local file system of this window), then the work, answered later to
// THIS window's page (localFsAnswer: the local file system is shared by the
// main window and the session windows, each gets its own answers).
func (b *nativeBridge) fsAsync(tok, reqID string, fn func() (interface{}, error)) (bool, error) {
	if !b.allowed(tok) || b.localfs == nil {
		return false, errNotAllowed
	}
	if !reqIDOK(reqID) {
		return false, errFsBadRequest
	}
	if err := b.localfs.runTo(reqID, fn, b.localFsAnswer); err != nil {
		return false, err
	}
	return true, nil
}

func (b *nativeBridge) fsSync(tok string) error {
	if !b.allowed(tok) || b.localfs == nil {
		return errNotAllowed
	}
	return nil
}

// fsAsyncMain / fsSyncMain: the main window's own calls (the editing round
// trip, the Files settings written, the editor picked): never a session
// window's, even with the shared local file system.
func (b *nativeBridge) fsAsyncMain(tok, reqID string, fn func() (interface{}, error)) (bool, error) {
	if b.localfsShared {
		return false, errNotAllowed
	}
	return b.fsAsync(tok, reqID, fn)
}

func (b *nativeBridge) fsSyncMain(tok string) error {
	if b.localfsShared {
		return errNotAllowed
	}
	return b.fsSync(tok)
}

func (b *nativeBridge) fsRoots(tok, reqID string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { r := b.localfs.sys.roots(); return &r, nil })
}

func (b *nativeBridge) fsList(tok, reqID, path string, o fsListOpts) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.list(path, o) })
}

func (b *nativeBridge) fsWalk(tok, reqID, dir string, names []string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.walk(dir, names) })
}

func (b *nativeBridge) fsOpenRead(tok, reqID, path string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.openRead(path) })
}

// fsWalkLarge / fsOpenReadLarge: the same, for a transfer streamed in
// chunks to a local SSH connection (no 100 MB per file).
func (b *nativeBridge) fsWalkLarge(tok, reqID, dir string, names []string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.walkLarge(dir, names) })
}

func (b *nativeBridge) fsOpenReadLarge(tok, reqID, path string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.openReadLarge(path) })
}

func (b *nativeBridge) fsRead(tok, reqID, handle string, offset int64, length int) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.read(handle, offset, length) })
}

func (b *nativeBridge) fsClose(tok, handle string) (bool, error) {
	if err := b.fsSync(tok); err != nil {
		return false, err
	}
	b.localfs.closeRead(handle)
	return true, nil
}

func (b *nativeBridge) fsWriteBegin(tok, reqID, dir, name string, o fsWriteOpts) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.writeBegin(dir, name, o) })
}

func (b *nativeBridge) fsWrite(tok, reqID, handle string, offset int64, data string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.write(handle, offset, data) })
}

func (b *nativeBridge) fsWriteEnd(tok, reqID, handle string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.writeEnd(handle) })
}

func (b *nativeBridge) fsWriteAbort(tok, handle string) (bool, error) {
	if err := b.fsSync(tok); err != nil {
		return false, err
	}
	go b.localfs.writeAbort(handle)
	return true, nil
}

func (b *nativeBridge) fsMkdir(tok, reqID, parent, name string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.mkdir(parent, name) })
}

func (b *nativeBridge) fsRename(tok, reqID, path, newName string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.rename(path, newName) })
}

func (b *nativeBridge) fsTrash(tok, reqID string, paths []string) (bool, error) {
	return b.fsAsync(tok, reqID, func() (interface{}, error) { return b.localfs.trash(paths) })
}

func (b *nativeBridge) fsReveal(tok, path string) (bool, error) {
	if err := b.fsSync(tok); err != nil {
		return false, err
	}
	if err := b.localfs.reveal(path); err != nil {
		return false, errors.New(fsCodeOf(err))
	}
	return true, nil
}

func (b *nativeBridge) filesSettingsGet(tok string) (filesSettingsView, error) {
	if err := b.fsSync(tok); err != nil {
		return filesSettingsView{}, err
	}
	return b.localfs.settingsView(), nil
}

func (b *nativeBridge) filesSettingsSet(tok string, in filesSettingsIn) (filesSettingsView, error) {
	if err := b.fsSyncMain(tok); err != nil {
		return filesSettingsView{}, err
	}
	return b.localfs.applySettings(in), nil
}

func (b *nativeBridge) filesPickEditor(tok, reqID string) (bool, error) {
	if !b.allowed(tok) || b.localfs == nil || b.localfsShared {
		return false, errNotAllowed
	}
	if !reqIDOK(reqID) {
		return false, errFsBadRequest
	}
	return true, b.localfs.pickEditorAsync(reqID)
}

func (b *nativeBridge) editBegin(tok, reqID string, o editBeginOpts) (bool, error) {
	return b.fsAsyncMain(tok, reqID, func() (interface{}, error) { return b.localfs.edits.begin(o) })
}

func (b *nativeBridge) editReload(tok, reqID, editID string, o fsWriteOpts) (bool, error) {
	return b.fsAsyncMain(tok, reqID, func() (interface{}, error) { return b.localfs.edits.reload(editID, o) })
}

func (b *nativeBridge) editLaunch(tok, reqID, editID string) (bool, error) {
	return b.fsAsyncMain(tok, reqID, func() (interface{}, error) { return b.localfs.edits.launch(editID) })
}

func (b *nativeBridge) editSnapshot(tok, reqID, editID string, seq int64) (bool, error) {
	return b.fsAsyncMain(tok, reqID, func() (interface{}, error) { return b.localfs.edits.snapshot(editID, seq) })
}

func (b *nativeBridge) editSynced(tok, editID string, seq int64) (bool, error) {
	if err := b.fsSyncMain(tok); err != nil {
		return false, err
	}
	return b.localfs.edits.synced(editID, seq), nil
}

func (b *nativeBridge) editStop(tok, reqID, editID string, o editStopOpts) (bool, error) {
	return b.fsAsyncMain(tok, reqID, func() (interface{}, error) { return b.localfs.edits.stop(editID, o) })
}

// editRemote: the device's file as the page last listed it (its baseline,
// found again after a reload).
func (b *nativeBridge) editRemote(tok, editID string, r editRemote) (bool, error) {
	if err := b.fsSyncMain(tok); err != nil {
		return false, err
	}
	return b.localfs.edits.setRemote(editID, r), nil
}

func (b *nativeBridge) editList(tok string) ([]editInfo, error) {
	if err := b.fsSyncMain(tok); err != nil {
		return nil, err
	}
	return b.localfs.edits.list(), nil
}
