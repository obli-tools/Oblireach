//go:build linux

package main

// connimport_linux.go — the Linux side of the connection tree's import /
// export (connimport_bridge.go): WinSCP's registry does not exist here
// (WinSCP.ini only, e.g. a file copied from Windows). The file dialogs are
// the FileChooser portal's (portal_files_linux.go linuxDialogs).

// winscpRegistrySessions: no registry on Linux ("winscp_not_found").
func winscpRegistrySessions() (sessions []winscpSession, master, found bool) {
	return nil, false, false
}
