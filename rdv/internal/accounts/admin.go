package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrLastAdmin refuses a change that would leave the instance without an
// enabled admin.
var ErrLastAdmin = errors.New("accounts: the last admin cannot be removed")

// DB is the underlying database (tests, diagnostics).
func (s *Store) DB() *sql.DB { return s.db }

// SetRetention sets how long the login and session history (and the admin
// audit log) are kept. Values <= 0 keep the defaults.
func (s *Store) SetRetention(history, audit time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if history > 0 {
		s.historyTTL = history
	}
	if audit > 0 {
		s.auditTTL = audit
	}
}

// ---- account changes ----

func (s *Store) exec1(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// enabledAdminsExcept counts the enabled admins other than id.
func enabledAdminsExcept(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE role = 'admin' AND disabled = 0 AND id <> ?`, id).Scan(&n)
	return n, err
}

// SetRole changes the role of an account. Demoting the last enabled admin
// is refused (ErrLastAdmin).
func (s *Store) SetRole(ctx context.Context, id int64, role string) error {
	if role != RoleAdmin && role != RoleOperator {
		return errors.New("accounts: invalid role")
	}
	if role == RoleOperator {
		a, err := s.ByID(ctx, id)
		if err != nil {
			return err
		}
		if a.IsAdmin() && !a.Disabled {
			if n, err := enabledAdminsExcept(ctx, s.db, id); err != nil {
				return err
			} else if n == 0 {
				return ErrLastAdmin
			}
		}
	}
	return s.exec1(ctx, `UPDATE accounts SET role = ? WHERE id = ?`, role, id)
}

// EnsureAdmins gives the admin role to the verified accounts of the listed
// addresses (RDV_ADMIN_EMAILS). Unverified accounts are left alone: without
// email verification anyone could register the address first.
func (s *Store) EnsureAdmins(ctx context.Context, emails []string) (int64, error) {
	var n int64
	for _, e := range emails {
		res, err := s.db.ExecContext(ctx, `UPDATE accounts SET role = 'admin' WHERE email = ? AND verified_at IS NOT NULL AND role <> 'admin'`, NormalizeEmail(e))
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, nil
}

// SetVerified marks the address of an account verified (by an admin) and
// drops its pending registrations.
func (s *Store) SetVerified(ctx context.Context, id int64) error {
	if err := s.exec1(ctx, `UPDATE accounts SET verified_at = COALESCE(verified_at, ?) WHERE id = ?`, unix(s.now()), id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending WHERE email = (SELECT email FROM accounts WHERE id = ?)`, id)
	return err
}

// TouchLogin records a sign-in (web pages) for the inactivity purge and the
// admin list.
func (s *Store) TouchLogin(ctx context.Context, id int64, ip string) error {
	return s.exec1(ctx, `UPDATE accounts SET last_login_at = ?, last_login_ip = ? WHERE id = ?`, unix(s.now()), ip, id)
}

// SetPassword replaces the password hash of an account.
func (s *Store) SetPassword(ctx context.Context, id int64, pwHash string) error {
	return s.exec1(ctx, `UPDATE accounts SET pw_hash = ? WHERE id = ?`, pwHash, id)
}

// SetName replaces the display name.
func (s *Store) SetName(ctx context.Context, id int64, name string) error {
	return s.exec1(ctx, `UPDATE accounts SET name = ? WHERE id = ?`, name, id)
}

// SetEmail replaces the address; verified tells whether the new address was
// proven (a link sent to it), else the account becomes unverified.
func (s *Store) SetEmail(ctx context.Context, id int64, email string, verified bool) error {
	var ver any
	if verified {
		ver = unix(s.now())
	}
	// A banned address is refused like a taken one.
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET email = ?, verified_at = ? WHERE id = ?
		AND NOT EXISTS (SELECT 1 FROM accounts WHERE email = ? AND id <> ?)
		AND NOT EXISTS (SELECT 1 FROM banned_emails WHERE email = ?)`, email, ver, id, email, id, email)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrExists
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.ByID(ctx, id); err != nil {
			return err
		}
		return ErrExists
	}
	return nil
}

// SetQuota stores the quota overrides of an account.
func (s *Store) SetQuota(ctx context.Context, id int64, q QuotaOverride) error {
	v := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	return s.exec1(ctx, `UPDATE accounts SET q_max_sessions = ?, q_sessions_day = ?, q_relay_bytes = ? WHERE id = ?`,
		v(q.MaxSessions), v(q.SessionsDay), v(q.RelayBytes), id)
}

// SetDisabledID is SetDisabled by account id. Disabling the last enabled
// admin is refused (ErrLastAdmin).
func (s *Store) SetDisabledID(ctx context.Context, id int64, disabled bool) error {
	a, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	if disabled && a.IsAdmin() && !a.Disabled {
		if n, err := enabledAdminsExcept(ctx, s.db, id); err != nil {
			return err
		} else if n == 0 {
			return ErrLastAdmin
		}
	}
	return s.SetDisabled(ctx, a.Email, disabled)
}

// DeleteID deletes an account by id (refused for the last enabled admin).
func (s *Store) DeleteID(ctx context.Context, id int64) error {
	a, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	if a.IsAdmin() && !a.Disabled {
		if n, err := enabledAdminsExcept(ctx, s.db, id); err != nil {
			return err
		} else if n == 0 {
			return ErrLastAdmin
		}
	}
	return s.Delete(ctx, a.Email)
}

// ---- Desktop tokens ----

// TokenInfo describes a Desktop login (never its secret).
type TokenInfo struct {
	ID       int64 // rowid, to revoke it
	Client   string
	Created  time.Time
	LastUsed time.Time
	LastIP   string
	Expires  time.Time
}

// Tokens lists the live Desktop logins of an account, most recent first.
func (s *Store) Tokens(ctx context.Context, accountID int64) ([]TokenInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rowid, client, created_at, last_used_at, last_ip, expires_at FROM tokens
		WHERE account_id = ? AND expires_at > ? ORDER BY last_used_at DESC, rowid DESC`, accountID, unix(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var t TokenInfo
		var c, u, e int64
		if err := rows.Scan(&t.ID, &t.Client, &c, &u, &t.LastIP, &e); err != nil {
			return nil, err
		}
		t.Created, t.LastUsed, t.Expires = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC(), time.Unix(e, 0).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeTokenID revokes one Desktop login of an account.
func (s *Store) RevokeTokenID(ctx context.Context, accountID, tokenID int64) error {
	return s.exec1(ctx, `DELETE FROM tokens WHERE rowid = ? AND account_id = ?`, tokenID, accountID)
}

// RevokeTokens revokes every Desktop login of an account.
func (s *Store) RevokeTokens(ctx context.Context, accountID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE account_id = ?`, accountID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---- web sessions ----

// WebSession is a signed-in browser of the account pages.
type WebSession struct {
	Hash     string
	Account  int64
	Created  time.Time
	LastSeen time.Time
	IP       string
	MFA      bool // the second factor was checked for this session
}

// webTouch is the minimum interval between two "last seen" updates.
const webTouch = time.Minute

// NewWebSession creates a web session and returns its secret (the cookie).
func (s *Store) NewWebSession(ctx context.Context, accountID int64, ip string, mfa bool) (string, error) {
	secret, hash := NewSecret("")
	now := unix(s.now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions(hash, account_id, created_at, last_seen_at, ip, mfa) VALUES(?,?,?,?,?,?)`,
		hash, accountID, now, now, ip, mfa)
	return secret, err
}

// ResolveWebSession returns a live web session (idle and absolute limits
// enforced) and its account; disabled accounts resolve to ErrNotFound.
func (s *Store) ResolveWebSession(ctx context.Context, secret string) (WebSession, Account, error) {
	if secret == "" || len(secret) > 128 {
		return WebSession{}, Account{}, ErrNotFound
	}
	now := s.now()
	ws := WebSession{Hash: HashSecret(secret)}
	var c, seen int64
	err := s.db.QueryRowContext(ctx, `SELECT account_id, created_at, last_seen_at, ip, mfa FROM web_sessions WHERE hash = ?`, ws.Hash).
		Scan(&ws.Account, &c, &seen, &ws.IP, &ws.MFA)
	if errors.Is(err, sql.ErrNoRows) {
		return WebSession{}, Account{}, ErrNotFound
	}
	if err != nil {
		return WebSession{}, Account{}, err
	}
	ws.Created, ws.LastSeen = time.Unix(c, 0).UTC(), time.Unix(seen, 0).UTC()
	if now.Sub(ws.Created) >= WebAbsoluteTTL || now.Sub(ws.LastSeen) >= WebIdleTTL {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE hash = ?`, ws.Hash)
		return WebSession{}, Account{}, ErrNotFound
	}
	a, err := s.ByID(ctx, ws.Account)
	if err != nil {
		return WebSession{}, Account{}, err
	}
	if a.Disabled {
		return WebSession{}, Account{}, ErrNotFound
	}
	if now.Sub(ws.LastSeen) >= webTouch {
		_, _ = s.db.ExecContext(ctx, `UPDATE web_sessions SET last_seen_at = ? WHERE hash = ?`, unix(now), ws.Hash)
		ws.LastSeen = now.UTC()
	}
	return ws, a, nil
}

// SetWebSessionMFA marks a session as having passed the second factor.
func (s *Store) SetWebSessionMFA(ctx context.Context, hash string) error {
	return s.exec1(ctx, `UPDATE web_sessions SET mfa = 1 WHERE hash = ?`, hash)
}

// DeleteWebSession ends one web session (by its hash).
func (s *Store) DeleteWebSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE hash = ?`, hash)
	return err
}

// DeleteWebSessions ends the web sessions of an account, except the one
// whose hash is keep ("" ends them all).
func (s *Store) DeleteWebSessions(ctx context.Context, accountID int64, keep string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE account_id = ? AND hash <> ?`, accountID, keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// WebSessions lists the live web sessions of an account, most recent first.
func (s *Store) WebSessions(ctx context.Context, accountID int64) ([]WebSession, error) {
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT hash, created_at, last_seen_at, ip, mfa FROM web_sessions
		WHERE account_id = ? AND created_at > ? AND last_seen_at > ? ORDER BY last_seen_at DESC`,
		accountID, unix(now.Add(-WebAbsoluteTTL)), unix(now.Add(-WebIdleTTL)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebSession
	for rows.Next() {
		ws := WebSession{Account: accountID}
		var c, seen int64
		if err := rows.Scan(&ws.Hash, &c, &seen, &ws.IP, &ws.MFA); err != nil {
			return nil, err
		}
		ws.Created, ws.LastSeen = time.Unix(c, 0).UTC(), time.Unix(seen, 0).UTC()
		out = append(out, ws)
	}
	return out, rows.Err()
}

// ---- TOTP ----

// SetTOTP stores the sealed TOTP secret of an account (nil removes it).
func (s *Store) SetTOTP(ctx context.Context, id int64, sealed []byte) error {
	var v any
	if sealed != nil {
		v = sealed
	}
	return s.exec1(ctx, `UPDATE accounts SET totp_secret = ?, totp_last_step = 0 WHERE id = ?`, v, id)
}

// TOTPSecret returns the sealed TOTP secret of an account (nil if none).
func (s *Store) TOTPSecret(ctx context.Context, id int64) ([]byte, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT totp_secret FROM accounts WHERE id = ?`, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// UseTOTPStep records a used time step; false when it (or a later one) was
// already used: a code is accepted once.
func (s *Store) UseTOTPStep(ctx context.Context, id, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ---- action links (password reset, set password, email change) ----

// Action link kinds.
const (
	ActionReset = "reset" // set a new password (forgot, admin reset, admin create)
	ActionEmail = "email" // confirm a new email address
)

// NewActionToken creates a single-use link secret for an account. Earlier
// links of the same kind are revoked.
func (s *Store) NewActionToken(ctx context.Context, accountID int64, kind, newEmail string, ttl time.Duration) (string, error) {
	secret, hash := NewSecret("")
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM action_tokens WHERE account_id = ? AND kind = ?`, accountID, kind); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO action_tokens(hash, account_id, kind, new_email, created_at, expires_at) VALUES(?,?,?,?,?,?)`,
		hash, accountID, kind, newEmail, unix(now), unix(now.Add(ttl))); err != nil {
		return "", err
	}
	return secret, tx.Commit()
}

// ActionToken returns the account and new address of a live link, without
// consuming it.
func (s *Store) ActionToken(ctx context.Context, secret, kind string) (int64, string, error) {
	if secret == "" || len(secret) > 128 {
		return 0, "", ErrNotFound
	}
	var id int64
	var email string
	err := s.db.QueryRowContext(ctx, `SELECT account_id, new_email FROM action_tokens WHERE hash = ? AND kind = ? AND expires_at > ?`,
		HashSecret(secret), kind, unix(s.now())).Scan(&id, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	return id, email, err
}

// ConsumeActionToken deletes a live link and returns what ActionToken
// returns; a link works once.
func (s *Store) ConsumeActionToken(ctx context.Context, secret, kind string) (int64, string, error) {
	id, email, err := s.ActionToken(ctx, secret, kind)
	if err != nil {
		return 0, "", err
	}
	if err := s.exec1(ctx, `DELETE FROM action_tokens WHERE hash = ?`, HashSecret(secret)); err != nil {
		return 0, "", err
	}
	return id, email, nil
}

// ---- login history ----

// Login kinds.
const (
	LoginWeb     = "web"
	LoginDesktop = "desktop"
)

// LoginRecord is one sign-in attempt on an existing account.
type LoginRecord struct {
	At     time.Time
	Kind   string
	OK     bool
	Reason string
	IP     string
}

// AddLogin records a sign-in attempt on an existing account.
func (s *Store) AddLogin(ctx context.Context, accountID int64, kind string, ok bool, reason, ip string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO login_history(account_id, at, kind, ok, reason, ip) VALUES(?,?,?,?,?,?)`,
		accountID, unix(s.now()), kind, ok, reason, ip)
	return err
}

// Logins returns the most recent sign-in attempts of an account.
func (s *Store) Logins(ctx context.Context, accountID int64, limit int) ([]LoginRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, kind, ok, reason, ip FROM login_history WHERE account_id = ? ORDER BY at DESC, id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoginRecord
	for rows.Next() {
		var r LoginRecord
		var at int64
		if err := rows.Scan(&at, &r.Kind, &r.OK, &r.Reason, &r.IP); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoginCounts counts the sign-in attempts since a time.
func (s *Store) LoginCounts(ctx context.Context, since time.Time) (ok, failed int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(ok), 0), COALESCE(SUM(1 - ok), 0) FROM login_history WHERE at >= ?`, unix(since)).Scan(&ok, &failed)
	return
}

// ---- session history ----

// SessionRecord is one ended session. The person helped is only known by
// a truncated host ID.
type SessionRecord struct {
	ID        int64
	AccountID int64  // 0 for a static operator key
	KeyLabel  string // static key label (AccountID == 0)
	Email     string // account address (read side only)
	HostTID   string
	Started   time.Time
	Accepted  time.Time // zero when the host never accepted
	Ended     time.Time
	Path      string
	Relayed   int64
	EndReason string
	// SID is the session id (links abuse reports to the session); ReportHash
	// the hash of the report token handed to the host (write side; empty
	// when the session was never accepted).
	SID        string
	ReportHash string
	// ReportID is the abuse report about this session (read side, 0 when
	// none).
	ReportID int64
}

// Duration is the connected time (from acceptance), 0 if never accepted.
func (r SessionRecord) Duration() time.Duration {
	if r.Accepted.IsZero() {
		return 0
	}
	return r.Ended.Sub(r.Accepted)
}

// AddSessionRecord stores an ended session.
func (s *Store) AddSessionRecord(ctx context.Context, r SessionRecord) error {
	var acct, acc any
	if r.AccountID != 0 {
		acct = r.AccountID
	}
	if !r.Accepted.IsZero() {
		acc = unix(r.Accepted)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_history(account_id, key_label, host_tid, started_at, accepted_at, ended_at, path, relay_bytes, end_reason, sid, report_hash)
		SELECT ?,?,?,?,?,?,?,?,?,?,? WHERE ? IS NULL OR EXISTS (SELECT 1 FROM accounts WHERE id = ?)`,
		acct, r.KeyLabel, r.HostTID, unix(r.Started), acc, unix(r.Ended), r.Path, r.Relayed, r.EndReason, r.SID, r.ReportHash, acct, acct)
	return err
}

// Sessions returns ended sessions, most recent first: of one account
// (accountID > 0), or of everyone (accountID == 0).
func (s *Store) Sessions(ctx context.Context, accountID int64, limit, offset int) ([]SessionRecord, error) {
	q := `SELECT h.id, COALESCE(h.account_id, 0), h.key_label, COALESCE(a.email, ''), h.host_tid, h.started_at, h.accepted_at,
		h.ended_at, h.path, h.relay_bytes, h.end_reason, h.sid,
		COALESCE((SELECT r.id FROM reports r WHERE h.sid <> '' AND r.sid = h.sid), 0)
		FROM session_history h LEFT JOIN accounts a ON a.id = h.account_id`
	args := []any{}
	if accountID > 0 {
		q += ` WHERE h.account_id = ?`
		args = append(args, accountID)
	}
	q += ` ORDER BY h.ended_at DESC, h.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var r SessionRecord
		var st, en int64
		var acc sql.NullInt64
		if err := rows.Scan(&r.ID, &r.AccountID, &r.KeyLabel, &r.Email, &r.HostTID, &st, &acc, &en, &r.Path, &r.Relayed, &r.EndReason, &r.SID, &r.ReportID); err != nil {
			return nil, err
		}
		r.Started, r.Accepted, r.Ended = time.Unix(st, 0).UTC(), fromUnix(acc), time.Unix(en, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionStats sums the accepted sessions ended since a time.
type SessionStats struct {
	Sessions int   // accepted sessions
	Direct   int   // of which direct
	Relay    int   // of which relayed
	Seconds  int64 // connected time
	Relayed  int64 // relayed bytes
	Failed   int   // sessions that never got accepted
}

// SessionStatsSince returns the stats of one account (accountID > 0) or of
// everyone (0).
func (s *Store) SessionStatsSince(ctx context.Context, accountID int64, since time.Time) (SessionStats, error) {
	q := `SELECT
		COALESCE(SUM(accepted_at IS NOT NULL), 0),
		COALESCE(SUM(accepted_at IS NOT NULL AND path = 'direct'), 0),
		COALESCE(SUM(accepted_at IS NOT NULL AND path LIKE 'relay%'), 0),
		COALESCE(SUM(CASE WHEN accepted_at IS NOT NULL THEN ended_at - accepted_at ELSE 0 END), 0),
		COALESCE(SUM(relay_bytes), 0),
		COALESCE(SUM(accepted_at IS NULL), 0)
		FROM session_history WHERE ended_at >= ?`
	args := []any{unix(since)}
	if accountID > 0 {
		q += ` AND account_id = ?`
		args = append(args, accountID)
	}
	var st SessionStats
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&st.Sessions, &st.Direct, &st.Relay, &st.Seconds, &st.Relayed, &st.Failed)
	return st, err
}

// DayStat is one UTC day of the session charts.
type DayStat struct {
	Day     string // YYYY-MM-DD
	Direct  int
	Relay   int
	Other   int // accepted, path unknown
	Relayed int64
}

// DailyStats returns the last days (oldest first, today included, every day
// present), for one account (> 0) or everyone (0).
func (s *Store) DailyStats(ctx context.Context, accountID int64, days int) ([]DayStat, error) {
	now := s.now().UTC()
	first := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	q := `SELECT date(ended_at, 'unixepoch'),
		COALESCE(SUM(accepted_at IS NOT NULL AND path = 'direct'), 0),
		COALESCE(SUM(accepted_at IS NOT NULL AND path LIKE 'relay%'), 0),
		COALESCE(SUM(accepted_at IS NOT NULL AND path <> 'direct' AND path NOT LIKE 'relay%'), 0),
		COALESCE(SUM(relay_bytes), 0)
		FROM session_history WHERE ended_at >= ?`
	args := []any{unix(first)}
	if accountID > 0 {
		q += ` AND account_id = ?`
		args = append(args, accountID)
	}
	q += ` GROUP BY 1`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]DayStat{}
	for rows.Next() {
		var d DayStat
		if err := rows.Scan(&d.Day, &d.Direct, &d.Relay, &d.Other, &d.Relayed); err != nil {
			return nil, err
		}
		byDay[d.Day] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DayStat, days)
	for i := range out {
		k := day(first.AddDate(0, 0, i))
		d := byDay[k]
		d.Day = k
		out[i] = d
	}
	return out, nil
}

// ---- audit log ----

// AuditEntry is one admin action.
type AuditEntry struct {
	At       time.Time
	ActorID  int64
	Actor    string // email of the admin, or "cli"
	Action   string
	TargetID int64
	Target   string // email of the target account
	Detail   string
	IP       string
}

// Audit appends an entry to the audit log.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(at, actor_id, actor, action, target_id, target, detail, ip) VALUES(?,?,?,?,?,?,?,?)`,
		unix(s.now()), e.ActorID, e.Actor, e.Action, e.TargetID, e.Target, e.Detail, e.IP)
	return err
}

// AuditLog returns entries, most recent first, and the total count.
func (s *Store) AuditLog(ctx context.Context, limit, offset int) ([]AuditEntry, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at, actor_id, actor, action, target_id, target, detail, ip FROM audit_log ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		if err := rows.Scan(&at, &e.ActorID, &e.Actor, &e.Action, &e.TargetID, &e.Target, &e.Detail, &e.IP); err != nil {
			return nil, 0, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// ---- admin listing and counters ----

// Counts are the account counters of the admin dashboard.
type Counts struct {
	Total, Verified, Disabled, Admins, NewWeek, Pending int
}

// AccountCounts returns the account counters.
func (s *Store) AccountCounts(ctx context.Context) (Counts, error) {
	now := s.now()
	var c Counts
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(verified_at IS NOT NULL), 0), COALESCE(SUM(disabled), 0),
		COALESCE(SUM(role = 'admin'), 0), COALESCE(SUM(created_at >= ?), 0),
		(SELECT COUNT(DISTINCT email) FROM pending WHERE expires_at > ?)
		FROM accounts`, unix(now.Add(-7*24*time.Hour)), unix(now)).Scan(&c.Total, &c.Verified, &c.Disabled, &c.Admins, &c.NewWeek, &c.Pending)
	return c, err
}

// WeeklySignups returns the accounts created per week, oldest first, the
// current week last.
func (s *Store) WeeklySignups(ctx context.Context, weeks int) ([]int, error) {
	now := s.now()
	out := make([]int, weeks)
	rows, err := s.db.QueryContext(ctx, `SELECT created_at FROM accounts WHERE created_at >= ?`, unix(now.Add(-time.Duration(weeks)*7*24*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c int64
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		i := weeks - 1 - int(now.Sub(time.Unix(c, 0))/(7*24*time.Hour))
		if i >= 0 && i < weeks {
			out[i]++
		}
	}
	return out, rows.Err()
}

// AccountRow is one line of the admin account list.
type AccountRow struct {
	Account
	Sessions30 int       // accepted sessions over 30 days
	RelayMonth int64     // relayed bytes this month (ended sessions)
	TokenUsed  time.Time // last use of a Desktop login (zero if none)
	// OpenReports counts the abuse reports about the account not closed yet.
	OpenReports int
}

// Sort keys of SearchAccounts.
var accountSorts = map[string]string{
	"name": "a.name COLLATE NOCASE", "email": "a.email", "created": "a.created_at", "login": "COALESCE(a.last_login_at, 0)",
	"sessions": "s30", "relay": "relay", "role": "a.role", "verified": "(a.verified_at IS NOT NULL)", "id": "a.id",
}

// ValidSort reports whether key is a sort key of SearchAccounts.
func ValidSort(key string) bool { _, ok := accountSorts[key]; return ok }

// SearchAccounts lists accounts matching q (email or name, substring),
// sorted, one page at a time, with the total number of matches.
func (s *Store) SearchAccounts(ctx context.Context, q, sortKey string, desc bool, limit, offset int) ([]AccountRow, int, error) {
	now := s.now()
	where, args := "", []any{}
	if q = strings.TrimSpace(q); q != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q))
		where = ` WHERE lower(a.email) LIKE ? ESCAPE '\' OR lower(a.name) LIKE ? ESCAPE '\'`
		args = append(args, "%"+esc+"%", "%"+esc+"%")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts a`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order, ok := accountSorts[sortKey]
	if !ok {
		order = accountSorts["created"]
		desc = true
	}
	dir := " ASC"
	if desc {
		dir = " DESC"
	}
	cols := `a.id, a.email, a.name, a.pw_hash, a.created_at, a.verified_at, a.last_login_at, a.disabled,
		a.last_login_ip, a.role, a.totp_secret IS NOT NULL, a.q_max_sessions, a.q_sessions_day, a.q_relay_bytes, a.review_at, a.banned`
	qa := append([]any{unix(now.Add(-30 * 24 * time.Hour)), month(now)}, args...)
	qa = append(qa, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+`,
		(SELECT COUNT(*) FROM session_history h WHERE h.account_id = a.id AND h.accepted_at IS NOT NULL AND h.ended_at >= ?) AS s30,
		COALESCE((SELECT relay_bytes FROM usage_month m WHERE m.account_id = a.id AND m.month = ?), 0) AS relay,
		COALESCE((SELECT MAX(last_used_at) FROM tokens t WHERE t.account_id = a.id), 0),
		(SELECT COUNT(*) FROM reports r WHERE r.account_id = a.id AND r.status = 'open') AS open_reports
		FROM accounts a`+where+` ORDER BY `+order+dir+`, a.id`+dir+` LIMIT ? OFFSET ?`, qa...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AccountRow
	for rows.Next() {
		var r AccountRow
		var used int64
		sc := scanFunc(func(dst ...any) error {
			return rows.Scan(append(dst, &r.Sessions30, &r.RelayMonth, &used, &r.OpenReports)...)
		})
		a, _, err := scanAccount(sc)
		if err != nil {
			return nil, 0, err
		}
		r.Account = a
		if used > 0 {
			r.TokenUsed = time.Unix(used, 0).UTC()
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

type scanFunc func(...any) error

func (f scanFunc) Scan(dst ...any) error { return f(dst...) }

// LastTokenUse returns the last use of any Desktop login of an account.
func (s *Store) LastTokenUse(ctx context.Context, accountID int64) (time.Time, error) {
	var used int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_used_at), 0) FROM tokens WHERE account_id = ?`, accountID).Scan(&used)
	if err != nil || used == 0 {
		return time.Time{}, err
	}
	return time.Unix(used, 0).UTC(), nil
}
