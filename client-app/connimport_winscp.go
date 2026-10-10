package main

// connimport_winscp.go — WinSCP sessions (WinSCP.ini, or the registry
// HKCU\Software\Martin Prikryl\WinSCP 2\Sessions: connimport_windows.go reads
// it into the same winscpSession values).
//
// Format (WinSCP's source, core/HierarchicalStorage.cpp, SessionData.cpp,
// Security.cpp):
//   - one section / registry key per session: [Sessions\<name>], <name>
//     munged PuTTY-style (%XX escapes of the UTF-8 bytes, a leading
//     "%EF%BB%BF" BOM when it is UTF-8; otherwise the ANSI code page) — "/"
//     in the unmunged name separates folders ("Folder/Sub/Session");
//   - string values are munged the same way (HostName, UserName,
//     PublicKeyFile...); integers plain (PortNumber, FSProtocol);
//   - FSProtocol: 0 SCP, 1 SFTP (SCP fallback), 2 SFTP only, 5 FTP, 6 WebDAV,
//     7 S3 (absent: SFTP); PortNumber absent: the protocol's default;
//   - Password: WinSCP's "simple" obfuscation (not encryption) keyed by
//     UserName+HostName (winscpDecryptPassword); with a master password
//     (Configuration\Security UseMasterPassword=1) the passwords are AES
//     encrypted with it instead: they are NOT imported (reported).
//   - "Default Settings" is WinSCP's template, not a session: skipped.

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// winscpSession: one session's values (name unmunged, values raw as stored).
type winscpSession struct {
	Name   string
	Values map[string]string // value name -> raw stored string
}

// WinSCP's simple password obfuscation constants (core/Security.h).
const (
	winscpPwMagic     = 0xA3
	winscpPwFlag      = 0xFF
	winscpPwInternal  = 0x00
	winscpPwExternal  = 0x01
	winscpPwInternal2 = 0x02
)

// winscpUnmunge: PuTTY's unescape_registry_key (%XX -> the byte), then UTF-8
// when it starts with a BOM, else Windows-1252 (WinSCP's ANSI fallback; the
// common Western code page, documented as an assumption).
func winscpUnmunge(s string) string {
	var raw []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) {
			if h, ok := hexNibble(s[i+1]); ok {
				if l, ok2 := hexNibble(s[i+2]); ok2 {
					raw = append(raw, h<<4|l)
					i += 2
					continue
				}
			}
		}
		raw = append(raw, c)
	}
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return string(raw[3:])
	}
	if utf8.Valid(raw) && !bytes.ContainsAny(raw, "\x80") {
		return string(raw)
	}
	return cp1252ToString(raw)
}

// cp1252ToString: Windows-1252 bytes as a string (0x80-0x9F mapped, the rest
// Latin-1).
func cp1252ToString(b []byte) string {
	var hi = [32]rune{0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
		0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178}
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(hi[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// winscpMunge: the reverse, for the WinSCP.ini export: PuTTY's
// escape_registry_key over the UTF-8 bytes (a BOM first when there is any
// non-ASCII byte, as WinSCP writes it).
func winscpMunge(s string, value bool) string {
	raw := []byte(s)
	nonASCII := false
	for _, c := range raw {
		if c >= 0x80 {
			nonASCII = true
			break
		}
	}
	if nonASCII {
		raw = append([]byte{0xEF, 0xBB, 0xBF}, raw...)
	}
	var sb strings.Builder
	for i, c := range raw {
		esc := c == ' ' || c == '\\' || c == '*' || c == '?' || c == '%' || c < ' ' || c > '~' || (c == '.' && i == 0)
		if value {
			// (a value: PuTTY escapes only what an INI line cannot hold)
			esc = c == '%' || c < ' ' || c > '~'
		}
		if esc {
			sb.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(c)>>4, 16)) + strings.ToUpper(strconv.FormatInt(int64(c)&15, 16)))
		} else {
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// winscpDecryptPassword: WinSCP's DecryptPassword (core/Security.cpp).
// stored: the hex text of the Password value; key: UserName+HostName.
// ok false: not decodable (or an "external" = master-password-encrypted
// value: external true).
func winscpDecryptPassword(stored, key string) (pw string, external, ok bool) {
	s := strings.TrimSpace(stored)
	next := func() (byte, bool) {
		if len(s) < 2 {
			return 0, false
		}
		h, ok1 := winscpHexVal(s[0])
		l, ok2 := winscpHexVal(s[1])
		s = s[2:]
		if !ok1 || !ok2 {
			return 0, false
		}
		return ^((h<<4 + l) ^ winscpPwMagic), true
	}
	flag, okf := next()
	if !okf {
		return "", false, false
	}
	length := -1
	if flag == winscpPwFlag {
		ver, okv := next()
		if !okv {
			return "", false, false
		}
		switch ver {
		case winscpPwInternal:
			l, okl := next()
			if !okl {
				return "", false, false
			}
			length = int(l)
		case winscpPwInternal2:
			a, oka := next()
			b, okb := next()
			if !oka || !okb {
				return "", false, false
			}
			length = int(a)<<8 + int(b)
		case winscpPwExternal:
			return "", true, false
		default:
			return "", false, false
		}
	} else {
		length = int(flag)
	}
	shift, oks := next()
	if !oks {
		return "", false, false
	}
	if len(s) < int(shift)*2 {
		return "", false, false
	}
	s = s[int(shift)*2:]
	out := make([]byte, 0, length)
	for i := 0; i < length; i++ {
		c, okc := next()
		if !okc {
			return "", false, false
		}
		out = append(out, c)
	}
	res := string(out)
	if flag == winscpPwFlag {
		if !strings.HasPrefix(res, key) {
			return "", false, false
		}
		res = res[len(key):]
	}
	if !utf8.ValidString(res) {
		return "", false, false
	}
	return res, false, true
}

func winscpHexVal(c byte) (byte, bool) {
	// (WinSCP writes "0123456789ABCDEF"; lower case accepted too)
	return hexNibble(c)
}

// winscpEncryptPassword: WinSCP's EncryptPassword with no random padding
// (shift 0): the tests' known vector, and nothing else uses it.
func winscpEncryptPassword(pw, key string) string {
	enc := func(c byte) string {
		v := (^c) ^ winscpPwMagic
		const hx = "0123456789ABCDEF"
		return string([]byte{hx[v>>4], hx[v&15]})
	}
	p := key + pw
	var sb strings.Builder
	sb.WriteString(enc(winscpPwFlag))
	if len(p) > 255 {
		sb.WriteString(enc(winscpPwInternal2))
		sb.WriteString(enc(byte(len(p) >> 8)))
		sb.WriteString(enc(byte(len(p) & 0xFF)))
	} else {
		sb.WriteString(enc(winscpPwInternal))
		sb.WriteString(enc(byte(len(p))))
	}
	sb.WriteString(enc(0))
	for i := 0; i < len(p); i++ {
		sb.WriteString(enc(p[i]))
	}
	return sb.String()
}

// parseWinSCPIni reads WinSCP.ini's text: its sessions, and whether a
// master password protects the passwords.
func parseWinSCPIni(data []byte) (sessions []winscpSession, master bool) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var cur *winscpSession
	inSecurity := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			sec := line[1 : len(line)-1]
			cur = nil
			inSecurity = strings.EqualFold(sec, `Configuration\Security`)
			if len(sec) > len(`Sessions\`) && strings.EqualFold(sec[:len(`Sessions\`)], `Sessions\`) {
				sessions = append(sessions, winscpSession{Name: winscpUnmunge(sec[len(`Sessions\`):]), Values: map[string]string{}})
				cur = &sessions[len(sessions)-1]
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k, v := strings.TrimSpace(line[:eq]), line[eq+1:]
		if inSecurity && strings.EqualFold(k, "UseMasterPassword") && strings.TrimSpace(v) == "1" {
			master = true
		}
		if cur != nil {
			cur.Values[k] = v
		}
	}
	return sessions, master
}

// winscpItems turns WinSCP sessions into import items. readKey reads a
// PublicKeyFile (nil: key files are not read). master: the passwords are
// master-password encrypted.
func winscpItems(sessions []winscpSession, master bool, readKey func(path string) (string, string)) *connImportSet {
	set := &connImportSet{}
	if master {
		set.Warnings = append(set.Warnings, connNoteMaster)
	}
	for _, s := range sessions {
		if strings.EqualFold(s.Name, "Default Settings") {
			continue
		}
		if len(set.Items) >= connImportMaxItems {
			set.Warnings = append(set.Warnings, "truncated")
			break
		}
		val := func(k string) string {
			for kk, v := range s.Values {
				if strings.EqualFold(kk, k) {
					return v
				}
			}
			return ""
		}
		str := func(k string) string { return winscpUnmunge(val(k)) }
		num := func(k string, def int) int {
			v := strings.TrimSpace(val(k))
			if v == "" {
				return def
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return -1
			}
			return n
		}
		parts := strings.Split(s.Name, "/")
		it := connImportItem{Name: parts[len(parts)-1], Folder: parts[:len(parts)-1]}
		host, user := str("HostName"), str("UserName")
		it.Host, it.Username = host, user
		proto := num("FSProtocol", 1)
		defPort := 22
		switch proto {
		case 0:
			it.Proto, it.Kind = "SCP", vaultKindSSH
		case 1, 2:
			it.Proto, it.Kind = "SFTP", vaultKindSSH
		case 5:
			it.Proto, defPort = "FTP", 21
			if num("Ftps", 0) > 0 {
				it.Proto = "FTPS"
			}
		case 6:
			it.Proto, defPort = "WebDAV", 80
		case 7:
			it.Proto, defPort = "S3", 443
		default:
			it.Proto = "?"
		}
		port := num("PortNumber", defPort)
		if port > 0 && port <= 65535 {
			it.Port = uint16(port)
		}
		if it.Kind == vaultKindSSH {
			if kf := str("PublicKeyFile"); strings.TrimSpace(kf) != "" {
				it.KeyAuth = true
				if readKey != nil {
					it.Secret, it.SecretNote = readKey(kf)
				} else {
					it.SecretNote = connNoteKeyUnreadable
				}
			} else if pw := strings.TrimSpace(val("Password")); pw != "" {
				if master {
					it.SecretNote = connNoteMaster
				} else if dec, ext, ok := winscpDecryptPassword(pw, user+host); ok {
					it.Secret = dec
				} else if ext {
					it.SecretNote = connNoteMaster
				} else {
					it.SecretNote = connNoteUndecodable
				}
			}
		}
		connCheckItem(&it)
		set.Items = append(set.Items, it)
	}
	connSortItems(set.Items)
	return set
}

// connReadKeyFile: a PublicKeyFile's private key, when it is an OpenSSH /
// PEM one (what the SSH client reads: ssh_session.go). PuTTY's .ppk (WinSCP's
// usual format) is not: reported, the operator converts it (PuTTYgen) or
// pastes the key. Returns (key, "") or ("", note).
func connReadKeyFile(path string) (string, string) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Size() > int64(vaultMaxKeyRunes) {
		return "", connNoteKeyUnreadable
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", connNoteKeyUnreadable
	}
	defer wipeBytesLocal(b)
	t := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
	if !strings.HasPrefix(t, "-----BEGIN ") || !strings.Contains(t, "PRIVATE KEY-----") {
		return "", connNoteKeyFormat
	}
	return t + "\n", ""
}

// winscpIniExport: the connection tree's SSH connections as a WinSCP.ini
// (sessions only, NO password: WinSCP would store them obfuscated only).
// RDP connections have no WinSCP equivalent: counted in skipped.
func winscpIniExport(nodes []connExportNode) (ini []byte, exported, skipped int) {
	byID := map[string]connExportNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	path := func(n connExportNode) string {
		var parts []string
		seen := map[string]bool{}
		for p := n.Parent; p != "" && !seen[p]; {
			seen[p] = true
			f, ok := byID[p]
			if !ok {
				break
			}
			parts = append([]string{strings.ReplaceAll(f.Name, "/", "_")}, parts...)
			p = f.Parent
		}
		return strings.Join(append(parts, strings.ReplaceAll(n.Name, "/", "_")), "/")
	}
	var sb strings.Builder
	sb.WriteString("; WinSCP sessions exported by Oblireach Desktop (no passwords)\r\n")
	for _, n := range nodes {
		if n.Type == "folder" {
			continue
		}
		if n.Type != "ssh" {
			skipped++
			continue
		}
		name := winscpMunge(path(n), false) // ("/" is kept: WinSCP's folder separator)
		sb.WriteString("\r\n[Sessions\\" + name + "]\r\n")
		sb.WriteString("HostName=" + winscpMunge(n.Host, true) + "\r\n")
		sb.WriteString("PortNumber=" + strconv.Itoa(n.Port) + "\r\n")
		if n.Username != "" {
			sb.WriteString("UserName=" + winscpMunge(n.Username, true) + "\r\n")
		}
		sb.WriteString("FSProtocol=2\r\n")
		exported++
	}
	return []byte(sb.String()), exported, skipped
}
