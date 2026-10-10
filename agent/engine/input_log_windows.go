//go:build windows

package engine

// inputLogFiles lists the input diagnostic files that PurgeInputLogOnce wipes
// (the live log and its one rotated generation). They live under
// C:\Windows\Temp, matching the C inputLog sink in input_windows.go.
func inputLogFiles() []string {
	return []string{
		`C:\Windows\Temp\oblireach-input.log`,
		`C:\Windows\Temp\oblireach-input.log.1`,
	}
}
