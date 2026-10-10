package main

// connimport.go — importing connections into the local connection tree
// (the vault's folder/ssh/rdp entries, vault.go) from other tools, and the
// shared model of the importers (connimport_winscp.go, connimport_1remote.go,
// connexport.go's own format). Pure logic, no Windows API: the WebView
// bindings are connimport_bridge.go, the native side (file dialogs, the
// registry) connimport_windows.go.
//
// Flow: a source is parsed into a connImportSet (every session found, the
// unsupported ones too, with the reason); the page is shown a PREVIEW of it
// (connImportPreview: never a secret, only "has a password: yes/no"); the
// operator ticks what to import and, for each duplicate of an existing
// connection, ignore / replace / duplicate; the commit writes the ticked
// items into the vault in one save (Vault.ImportConnections, vault_conn.go)
// and answers a report (imported / skipped and why).
//
// Secrets (passwords, private keys) read from a source live only in the Go
// side's pending set (connImportPending) until the commit seals them into
// the vault (encrypted, DPAPI-protected: vault_windows.go) — they never go
// to the page, never to a log, never to the connection tree's metadata.

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// connImportItem: one session read from a source.
type connImportItem struct {
	Folder   []string // folder path below the import's target folder (may be empty)
	Name     string
	Proto    string // the source's protocol name, shown as is (SFTP, SCP, FTP, RDP, VNC...)
	Kind     byte   // vaultKindSSH / vaultKindRDP; 0: not supported (Skip says why)
	Host     string
	Port     uint16
	Username string
	// The secret, when the source had one and could be read: a password, or
	// for KeyAuth the private key's text (OpenSSH / PEM). Never sent to the page.
	Secret  string
	KeyAuth bool
	// Skip: why this item cannot be imported at all ("" = it can):
	// unsupported_protocol, no_host, bad_port.
	Skip string
	// SecretNote: why the secret is not imported although the session had
	// one (master_password, encrypted_source, undecodable, key_unreadable,
	// key_format, too_long). "" when there is none to import or it was read.
	SecretNote string
}

// connImportSet: what a source gave.
type connImportSet struct {
	Source   string // winscp-file, winscp-registry, 1remote, oblireach
	Items    []connImportItem
	Warnings []string // codes: master_password, wal_present, truncated...
}

// Item skip / secret note codes (the page translates connImport.reason.<code>).
const (
	connSkipUnsupported   = "unsupported_protocol"
	connSkipNoHost        = "no_host"
	connSkipBadPort       = "bad_port"
	connNoteMaster        = "master_password"
	connNoteEncrypted     = "encrypted_source"
	connNoteUndecodable   = "undecodable"
	connNoteKeyUnreadable = "key_unreadable"
	connNoteKeyFormat     = "key_format"
	connNoteTooLong       = "too_long"
)

const (
	connImportMaxItems    = 5000
	connImportMaxFolder   = 16 // nesting levels kept (deeper ones are merged into the 16th)
	connImportPendingTTL  = 15 * time.Minute
	connImportPendingMax  = 4
	connImportMaxFileSize = 64 << 20
)

// connCheckItem fills Skip for what cannot become a vault connection, and
// cleans the texts (labels are limited and control characters refused by
// vault.go: they are cut / replaced here so a long or odd name never makes a
// whole item fail).
func connCheckItem(it *connImportItem) {
	it.Name = connCleanText(it.Name, vaultMaxLabelRunes)
	it.Host = strings.TrimSpace(connCleanText(it.Host, vaultMaxHostRunes))
	it.Username = connCleanText(it.Username, vaultMaxUsernameRunes)
	for i := range it.Folder {
		it.Folder[i] = connCleanText(it.Folder[i], vaultMaxLabelRunes)
	}
	// (empty folder names: "a//b" or a trailing "/")
	f := it.Folder[:0]
	for _, s := range it.Folder {
		if strings.TrimSpace(s) != "" {
			f = append(f, strings.TrimSpace(s))
		}
	}
	it.Folder = f
	if len(it.Folder) > connImportMaxFolder {
		it.Folder = append(it.Folder[:connImportMaxFolder-1], strings.Join(it.Folder[connImportMaxFolder-1:], " / "))
		it.Folder[len(it.Folder)-1] = connCleanText(it.Folder[len(it.Folder)-1], vaultMaxLabelRunes)
	}
	if strings.TrimSpace(it.Name) == "" {
		it.Name = it.Host
		if it.Name == "" {
			it.Name = "?"
		}
	}
	if it.Kind != vaultKindSSH && it.Kind != vaultKindRDP {
		it.Kind = 0
		if it.Skip == "" {
			it.Skip = connSkipUnsupported
		}
	}
	if it.Skip == "" && it.Host == "" {
		it.Skip = connSkipNoHost
	}
	if it.Skip == "" && it.Port == 0 {
		it.Skip = connSkipBadPort
	}
	if it.Kind == vaultKindRDP {
		it.KeyAuth = false // (RDP: password only, vault.go)
	}
	if it.Secret != "" {
		max := vaultMaxSecretRunes
		if it.KeyAuth {
			max = vaultMaxKeyRunes
		}
		if len([]rune(it.Secret)) > max || hasControlRunesInSecret(it.Secret, it.KeyAuth) {
			it.Secret = ""
			it.SecretNote = connNoteTooLong
		}
	}
}

// connCleanText: control characters become spaces, the text is trimmed and
// cut to max runes.
func connCleanText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// connDupKey: what makes two connections "the same" (host + port + user +
// type; the host's case does not count).
func connDupKey(kind byte, host string, port uint16, user string) string {
	return string([]byte{kind}) + "\x00" + strings.ToLower(strings.TrimSpace(host)) + "\x00" +
		itoa(int(port)) + "\x00" + strings.TrimSpace(user)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ── Preview ─────────────────────────────────────────────────────────────────

// connPreviewItem: one item as the page sees it — never the secret.
type connPreviewItem struct {
	Ref         int    `json:"ref"`
	Folder      string `json:"folder"` // "a / b"
	Name        string `json:"name"`
	Proto       string `json:"proto"`
	Type        string `json:"type"` // ssh / rdp / ""
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	KeyAuth     bool   `json:"keyAuth"`
	HasPassword bool   `json:"hasPassword"` // a secret will be imported (with "import passwords")
	SecretNote  string `json:"secretNote"`
	Skip        string `json:"skip"`
	Dup         string `json:"dup"`     // id of the existing connection it duplicates ("": none)
	DupName     string `json:"dupName"` // that connection's name
}

type connPreview struct {
	ImportID       string            `json:"importId"`
	Source         string            `json:"source"`
	NeedPassphrase bool              `json:"needPassphrase"`
	Error          string            `json:"error"`
	Warnings       []string          `json:"warnings"`
	Items          []connPreviewItem `json:"items"`
}

func connTypeName(k byte) string {
	switch k {
	case vaultKindSSH:
		return "ssh"
	case vaultKindRDP:
		return "rdp"
	}
	return ""
}

// connBuildPreview: the page's view of set, each item marked when it
// duplicates one of existing (the vault's connections now) or an EARLIER
// item of the same set (Dup "set:<ref>").
func connBuildPreview(id string, set *connImportSet, existing []VaultEntryMeta) connPreview {
	out := connPreview{ImportID: id, Source: set.Source, Warnings: append([]string{}, set.Warnings...), Items: []connPreviewItem{}}
	have := map[string]VaultEntryMeta{}
	for _, m := range existing {
		if m.Kind == vaultKindSSH || m.Kind == vaultKindRDP {
			k := connDupKey(m.Kind, m.Host, m.Port, m.Username)
			if _, ok := have[k]; !ok {
				have[k] = m
			}
		}
	}
	for i, it := range set.Items {
		p := connPreviewItem{Ref: i, Folder: strings.Join(it.Folder, " / "), Name: it.Name, Proto: it.Proto, Type: connTypeName(it.Kind),
			Host: it.Host, Port: int(it.Port), Username: it.Username, KeyAuth: it.KeyAuth,
			HasPassword: it.Secret != "", SecretNote: it.SecretNote, Skip: it.Skip}
		if it.Skip == "" {
			if m, ok := have[connDupKey(it.Kind, it.Host, it.Port, it.Username)]; ok {
				p.Dup, p.DupName = m.ID, m.Label
			}
		}
		out.Items = append(out.Items, p)
	}
	return out
}

// ── Commit ──────────────────────────────────────────────────────────────────

// connDecision: the page's choice for one item.
type connDecision struct {
	Ref    int    `json:"ref"`
	Action string `json:"action"` // import (new), replace (the duplicate), duplicate (a new one next to it), skip
}

type connCommitOptions struct {
	ImportSecrets bool           `json:"importSecrets"`
	ParentID      string         `json:"parentId"` // the folder the import goes into ("" = root)
	Decisions     []connDecision `json:"decisions"`
}

// connReportLine: one item of the final report.
type connReportLine struct {
	Ref    int    `json:"ref"`
	Name   string `json:"name"`
	Result string `json:"result"` // imported, replaced, skipped, failed
	Reason string `json:"reason"` // the skip / failure reason code ("" otherwise)
	Secret bool   `json:"secret"` // a secret was stored with it
}

type connReport struct {
	Imported int              `json:"imported"`
	Replaced int              `json:"replaced"`
	Skipped  int              `json:"skipped"`
	Failed   int              `json:"failed"`
	Folders  int              `json:"folders"` // folders created
	Lines    []connReportLine `json:"lines"`
}

// Report reason codes (besides the item Skip codes).
const (
	connReasonNotSelected = "not_selected"
	connReasonDupIgnored  = "duplicate_ignored"
	connReasonInvalid     = "invalid"
	connReasonFull        = "vault_full"
	connReasonDupGone     = "duplicate_gone"
)

// connPlanItem: what the commit does with one item (connPlan's output,
// applied by Vault.ImportConnections).
type connPlanItem struct {
	Ref     int
	Item    connImportItem
	Replace string // the id of the connection to replace ("" = a new one)
}

// connPlan turns the decisions into the work list and the report lines of
// what is skipped. dupOf: the duplicate (an existing connection id) of each
// ref, as the preview said. Unknown refs and refs named twice are ignored
// (first wins); an item with no decision is not imported.
func connPlan(set *connImportSet, opts connCommitOptions, dupOf map[int]string) ([]connPlanItem, []connReportLine) {
	dec := map[int]string{}
	for _, d := range opts.Decisions {
		if d.Ref < 0 || d.Ref >= len(set.Items) {
			continue
		}
		if _, seen := dec[d.Ref]; !seen {
			dec[d.Ref] = d.Action
		}
	}
	var plan []connPlanItem
	var skipped []connReportLine
	for i, it := range set.Items {
		act, ok := dec[i]
		line := connReportLine{Ref: i, Name: it.Name, Result: "skipped"}
		switch {
		case it.Skip != "":
			line.Reason = it.Skip
		case !ok || act == "skip" || act == "":
			line.Reason = connReasonNotSelected
			if ok && act == "skip" && dupOf[i] != "" {
				line.Reason = connReasonDupIgnored
			}
		case act == "replace" && dupOf[i] != "":
			if !opts.ImportSecrets {
				it.Secret = ""
			}
			plan = append(plan, connPlanItem{Ref: i, Item: it, Replace: dupOf[i]})
			continue
		case act == "import" || act == "duplicate" || act == "replace":
			// (replace without a duplicate: a new one)
			if !opts.ImportSecrets {
				it.Secret = ""
			}
			plan = append(plan, connPlanItem{Ref: i, Item: it})
			continue
		default:
			line.Reason = connReasonNotSelected
		}
		skipped = append(skipped, line)
	}
	return plan, skipped
}

// ── Pending sets (between the preview and the commit) ───────────────────────

type connPendingEntry struct {
	set     *connImportSet
	dupOf   map[int]string
	created time.Time
	// encrypted: an Oblireach export whose secrets wait for the passphrase.
	encrypted *connExportFile
}

type connPendingStore struct {
	mu  sync.Mutex
	m   map[string]*connPendingEntry
	now func() time.Time
}

var connImportPending = &connPendingStore{m: map[string]*connPendingEntry{}, now: time.Now}

// put keeps e under a new id (the oldest goes beyond connImportPendingMax;
// expired ones go first). Returns the id.
func (s *connPendingStore) put(e *connPendingEntry) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	for len(s.m) >= connImportPendingMax {
		oldest, ot := "", time.Time{}
		for k, v := range s.m {
			if oldest == "" || v.created.Before(ot) {
				oldest, ot = k, v.created
			}
		}
		s.dropLocked(oldest)
	}
	id := newProfileID()
	e.created = s.now()
	s.m[id] = e
	return id
}

func (s *connPendingStore) get(id string) *connPendingEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	return s.m[id]
}

// take removes and returns id's entry (a commit uses a set once).
func (s *connPendingStore) take(id string) *connPendingEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	e := s.m[id]
	delete(s.m, id)
	return e
}

func (s *connPendingStore) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(id)
}

func (s *connPendingStore) dropLocked(id string) {
	if e := s.m[id]; e != nil && e.set != nil {
		for i := range e.set.Items {
			e.set.Items[i].Secret = ""
		}
	}
	delete(s.m, id)
}

func (s *connPendingStore) gcLocked() {
	now := s.now()
	for k, v := range s.m {
		if now.Sub(v.created) > connImportPendingTTL {
			s.dropLocked(k)
		}
	}
}

// connSortItems: folders' order then names, as the tree shows them.
func connSortItems(items []connImportItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := strings.Join(items[i].Folder, "/"), strings.Join(items[j].Folder, "/")
		if a != b {
			return strings.ToLower(a) < strings.ToLower(b)
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
}
