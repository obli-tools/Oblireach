//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// config.json: protected DACL (nothing inherited from ProgramData, whose
	// ACL grants BUILTIN\Users read), SYSTEM + Administrators full control
	// only.
	secretFileSDDL = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"

	// HKLM\SOFTWARE\OblireachAgent stays readable by Users: the capture
	// helper (which may run with the user's token) reads the
	// KeepAeroDuringSession opt-out there (dwm_windows.go). Older MSIs also
	// stored ApiKey in it: that value is deleted at startup.
	agentRegistryKeyPath = `SOFTWARE\OblireachAgent`
	// HKLM\SOFTWARE\OblireachAgent\Config: ServerUrl + ApiKey written by the
	// MSI (installer/product.wxs, PermissionEx with the same SDDL), protected
	// DACL, SYSTEM + Administrators only. Re-applied at every start.
	agentSecretKeyPath = agentRegistryKeyPath + `\Config`
	agentRegistrySDDL  = "D:P(A;CI;KA;;;SY)(A;CI;KA;;;BA)"

	agentServiceName = "OblireachAgent"
)

// writeSecretFile writes path so that only SYSTEM and Administrators can
// read it: a temp file is CREATED with the protected DACL (never readable by
// Users, not even briefly) and then renamed over path, which also replaces a
// file that a user planted beforehand (the directory lets Users create files;
// its owner could otherwise re-grant itself access). If the rename fails
// (target held open without FILE_SHARE_DELETE), the file is rewritten in place
// and protected explicitly.
func writeSecretFile(path string, data []byte) error {
	err := writeSecretFileReplace(path, data)
	if err == nil {
		return nil
	}
	if perr := protectSecretFile(path); perr != nil && !os.IsNotExist(perr) {
		return fmt.Errorf("%v; in-place protect: %v", err, perr)
	}
	if werr := os.WriteFile(path, data, 0600); werr != nil {
		return fmt.Errorf("%v; in-place write: %v", err, werr)
	}
	return protectSecretFile(path)
}

func writeSecretFileReplace(path string, data []byte) error {
	sd, err := windows.SecurityDescriptorFromString(secretFileSDDL)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	tmp := path + ".tmp-" + randomHex(6)
	p16, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	// CREATE_NEW: never reuse a file someone else created under that name.
	h, err := windows.CreateFile(p16, windows.GENERIC_WRITE, 0, sa,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// MoveFileEx(MOVEFILE_REPLACE_EXISTING): the moved file keeps its own
	// security descriptor.
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// protectSecretFile applies the protected SYSTEM + Administrators DACL to an
// existing file (drops the ACEs inherited from ProgramData, BUILTIN\Users
// read included). It also tries to make Administrators the owner, so a
// user-owned file cannot be re-opened by its owner; that part is best effort
// (it needs an elevated / SYSTEM token).
func protectSecretFile(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(secretFileSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if admins, aerr := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid); aerr == nil {
		if windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			admins, nil, dacl, nil) == nil {
			return nil
		}
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

// hardenSecretFile: Windows flavour of the in-place fallback.
func hardenSecretFile(path string) error {
	err := protectSecretFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// loadConfigFromRegistry returns the ServerUrl / ApiKey the MSI wrote under
// HKLM\SOFTWARE\OblireachAgent\Config ("" when absent or empty: a manual
// msiexec without SERVERURL / APIKEY writes empty values).
func loadConfigFromRegistry() (serverURL, apiKey string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, agentSecretKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", ""
	}
	defer k.Close()
	serverURL, _, _ = k.GetStringValue("ServerUrl")
	apiKey, _, _ = k.GetStringValue("ApiKey")
	return strings.TrimSpace(serverURL), strings.TrimSpace(apiKey)
}

// protectAgentRegistrySecrets applies the protected SYSTEM + Administrators
// DACL to HKLM\SOFTWARE\OblireachAgent\Config (the MSI sets the same DACL at
// install; this covers a key whose DACL was changed since) and deletes the
// ApiKey value older MSIs left in the Users-readable parent key.
func protectAgentRegistrySecrets() error {
	var errs []string
	if err := protectRegistryKey(registry.LOCAL_MACHINE, agentSecretKeyPath); err != nil {
		errs = append(errs, err.Error())
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, agentRegistryKeyPath, registry.SET_VALUE); err == nil {
		if derr := k.DeleteValue("ApiKey"); derr != nil && derr != registry.ErrNotExist {
			errs = append(errs, "delete legacy ApiKey value: "+derr.Error())
		}
		k.Close()
	} else if err != registry.ErrNotExist {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// protectRegistryKey applies agentRegistrySDDL to root\path (no-op when the
// key does not exist).
func protectRegistryKey(root registry.Key, path string) error {
	k, err := registry.OpenKey(root, path, windows.READ_CONTROL|windows.WRITE_DAC)
	if err == registry.ErrNotExist {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	sd, err := windows.SecurityDescriptorFromString(agentRegistrySDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(windows.Handle(k), windows.SE_REGISTRY_KEY,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

// scrubServiceArguments removes `--url ... --key ...` from the OblireachAgent
// service command line (ImagePath, readable by every local user) once the key
// is safely in config.json. MSIs from this version on register the service
// without arguments; this covers services registered by older MSIs or by
// hand. No-op when the line carries no key or the service does not exist.
func scrubServiceArguments() (bool, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, err
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(agentServiceName)
	svc, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_CHANGE_CONFIG)
	if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseServiceHandle(svc)

	var needed uint32
	_ = windows.QueryServiceConfig(svc, nil, 0, &needed)
	if needed == 0 || needed > 64*1024 {
		return false, fmt.Errorf("QueryServiceConfig: unexpected size %d", needed)
	}
	buf := make([]uint64, (needed+7)/8) // 8-byte aligned
	cfg := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if err := windows.QueryServiceConfig(svc, cfg, needed, &needed); err != nil {
		return false, err
	}
	stripped, ok := stripServiceSecretArgs(windows.UTF16PtrToString(cfg.BinaryPathName))
	if !ok {
		return false, nil
	}
	p, err := windows.UTF16PtrFromString(stripped)
	if err != nil {
		return false, err
	}
	if err := windows.ChangeServiceConfig(svc, windows.SERVICE_NO_CHANGE, windows.SERVICE_NO_CHANGE,
		windows.SERVICE_NO_CHANGE, p, nil, nil, nil, nil, nil, nil); err != nil {
		return false, err
	}
	return true, nil
}
