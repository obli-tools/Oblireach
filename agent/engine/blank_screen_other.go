//go:build !windows

package engine

// The blank screen exists on Windows only for now (blank_screen_windows.go):
// macOS / Linux agents never advertise it, and answer a request anyway with
// blank_screen_status{active:false, reason:"platform"}.

func platformBlankScreenAvailable() (bool, string)   { return false, "platform" }
func platformBlankScreenShow(blankScreenText) string { return "platform" }
func platformBlankScreenHide()                       {}
func platformBlankScreenLang() string                { return "en" }
