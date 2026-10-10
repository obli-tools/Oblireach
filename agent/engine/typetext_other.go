//go:build !windows

package engine

// The vault's type_text path is Windows-only (it needs the remote keyboard
// layout, the secure-desktop check and SendInput). Off Windows the feature is
// not advertised, so viewers hide the vault key button, and a type_text frame
// that arrives anyway is dropped by the direct input path (IsTypeTextMsg in
// dispatchInputJSON).
func typeTextSupported() bool { return false }
