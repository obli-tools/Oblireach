//go:build linux

package main

// editsession_linux.go — the editor of the editing round trip on Linux
// (editsession.go is platform-neutral: the working copies, their watch every
// 700 ms, the upload). The working copy is handed to the OpenURI portal
// with writable=true: the application it opens in (inside the Flatpak: a
// Documents portal path of OUR file) saves into the working copy itself, so
// the neutral watch sees every save, no inotify needed.
//
// A sandboxed app cannot start a program of the host, so the three choices
// of Settings > Files become:
//
//   - "notepad" (the default, "a text editor"): a plain text type opens in
//     its default application without a question; any other type goes
//     through the portal's application chooser (ask), so a type whose
//     default application would run it (a script, a .desktop file...) never
//     starts without the operator choosing an editor;
//   - "system": the type's default application, except those same types
//     (chooser);
//   - "custom": the portal's application chooser every time (the program
//     picked there; pickProgram picks none: portal_files_linux.go).
//
// The working copy is 0644 without an execute bit (localfs.go), whatever
// the application chosen.

import (
	"errors"
	"path/filepath"
	"strings"
)

// editTextExts: types that are plain text everywhere (their default
// application is an editor or a viewer, never an interpreter).
var editTextExts = map[string]bool{
	".txt": true, ".log": true, ".md": true, ".csv": true, ".tsv": true, ".json": true, ".xml": true, ".yml": true,
	".yaml": true, ".toml": true, ".ini": true, ".cfg": true, ".conf": true, ".config": true, ".properties": true,
	".env": true, ".sql": true, ".css": true, ".diff": true, ".patch": true, ".rst": true, ".tex": true, ".nfo": true,
	".c": true, ".h": true, ".cpp": true, ".hpp": true, ".go": true, ".rs": true, ".java": true, ".cs": true, ".ts": true,
	".lst": true, ".list": true, ".repo": true, ".service": true, ".timer": true, ".rules": true, ".pem": true,
}

// editRunsExts: types the desktop may RUN when "opened" (a script, a
// launcher, a package): never handed to their default application.
var editRunsExts = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".csh": true, ".ksh": true, ".py": true, ".pyw": true,
	".pl": true, ".rb": true, ".php": true, ".lua": true, ".tcl": true, ".js": true, ".mjs": true, ".jar": true,
	".desktop": true, ".appimage": true, ".run": true, ".bin": true, ".flatpakref": true, ".flatpakrepo": true,
	".rpm": true, ".deb": true, ".exe": true, ".msi": true, ".bat": true, ".cmd": true, ".ps1": true, ".vbs": true,
	".html": true, ".htm": true, ".svg": true, ".xhtml": true, ".url": true, ".webloc": true, ".kdesktop": true,
}

// editAsk: whether the chooser is shown for this file with this choice.
func editAsk(file, editor string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	switch editor {
	case "custom":
		return true
	case "system":
		return ext == "" || editRunsExts[ext]
	}
	return !editTextExts[ext] || editRunsExts[ext]
}

// The launcher's call (the tests watch it instead of the portal).
var editOpenURI = openURIFile

// plannedEditor: the editor launchEditor uses: notepad (a text editor, or
// the chooser), system, custom (the chooser).
func plannedEditor(file string, s FilesSettings) string {
	switch s.Editor {
	case "system", "custom":
		return s.Editor
	}
	return "notepad"
}

// launchEditor: the working copy in the application of Settings > Files.
func launchEditor(e *editSession, s FilesSettings) (*editLaunchResult, error) {
	editor := plannedEditor(e.file, s)
	if err := editOpenURI(0, e.file, true, editAsk(e.file, editor)); err != nil {
		if errors.Is(err, errPortalCancel) {
			return nil, fsE("cancelled")
		}
		return nil, fsE("no_editor")
	}
	return &editLaunchResult{Editor: editor}, nil
}

// editorDisplayName: the program's file name.
func editorDisplayName(p string) string {
	return strings.TrimSpace(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
}
