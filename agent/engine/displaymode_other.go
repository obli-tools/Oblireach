//go:build !windows

package engine

// The resolution selector is Windows-only (EnumDisplaySettingsEx /
// ChangeDisplaySettingsEx in the capture helper). Elsewhere display_modes is
// not advertised, so the viewers show no resolution control; the direct
// stream path ignores the requests (unknown message types are not input).
func displayModesSupported() bool { return false }
