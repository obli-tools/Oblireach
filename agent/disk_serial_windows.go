//go:build windows

package main

// readSystemDiskSerial for device identity v2 (device_identity.go). Same
// value as the Obliance agent's machine_uuid_windows.go (the disk fingerprint
// must be identical in every Obli* agent on one machine, or one agent would
// see a "changed" disk and treat the machine as a clone). Obliance reads the
// index-0 fallback through native WMI COM; Oblireach has no WMI COM helper, so
// it reads the very same Win32_DiskDrive.SerialNumber through Get-WmiObject
// (PowerShell 2.0+, Windows 7 / 2008 R2 included).

import (
	"os/exec"
	"strings"
	"syscall"
)

// diskSerialCmd: exec.Command without a console window (service context).
func diskSerialCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// readSystemDiskSerial returns the hardware serial number of the physical
// disk that hosts the Windows system volume (usually C:\).
//
// Walks SystemDrive -> Partition -> Disk via the Storage PowerShell module
// (Windows 8 / Server 2012 and later) to find the physical disk number, then
// reads SerialNumber from Win32_DiskDrive via CIM. Falls back to the serial of
// disk Index 0 (WMI, then wmic). Returns "" if every step fails.
func readSystemDiskSerial() string {
	script := `
$ErrorActionPreference = "SilentlyContinue"
$letter = ($env:SystemDrive).TrimEnd(':')
$partition = Get-Partition -DriveLetter $letter | Select-Object -First 1
if (-not $partition) { exit 1 }
$disk = Get-Disk -Number $partition.DiskNumber
if (-not $disk) { exit 1 }
$drive = Get-CimInstance -ClassName Win32_DiskDrive | Where-Object { $_.Index -eq $disk.Number } | Select-Object -First 1
if ($drive -and $drive.SerialNumber) { Write-Output $drive.SerialNumber.Trim() }
`
	out, err := diskSerialCmd("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err == nil {
		serial := strings.TrimSpace(string(out))
		if serial != "" {
			return serial
		}
	}

	// Fallback (Windows 7 / 2008 R2: no Storage module; or the script above
	// failed): serial of disk Index 0 from WMI — the value
	// `wmic diskdrive where Index=0 get SerialNumber` prints. Assumes disk 0
	// is the system disk, true on the vast majority of installs.
	wmiScript := `$ErrorActionPreference = "Stop"
$d = Get-WmiObject -Class Win32_DiskDrive -Filter "Index = 0" | Select-Object -First 1
if ($d -and $d.SerialNumber) { Write-Output ([string]$d.SerialNumber).Trim() } else { Write-Output "" }
`
	out, err = diskSerialCmd("powershell", "-NoProfile", "-NonInteractive", "-Command", wmiScript).Output()
	if err == nil {
		// WMI answered: its value (possibly empty) is authoritative, as in
		// the Obliance agent's native WMI path.
		return strings.TrimSpace(string(out))
	}

	// Last resort, WMI unusable from PowerShell: wmic (absent from recent
	// Windows images).
	out, err = diskSerialCmd("wmic", "diskdrive", "where", "Index=0", "get", "SerialNumber", "/value").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "SerialNumber=") {
				return strings.TrimSpace(strings.TrimPrefix(line, "SerialNumber="))
			}
		}
	}

	return ""
}
