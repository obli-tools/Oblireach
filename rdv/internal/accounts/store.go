// Package accounts keeps the operator accounts of an oblireach-rdv instance
// in a SQLite database (pure Go driver: the binary stays CGO-free and runs in
// a distroless static image).
//
// Data minimisation: an account is an email address, a display
// name, a password hash, its creation / verification / last-login dates, a
// role, a disabled flag, optional quota overrides, an optional TOTP secret
// (sealed by the server) and usage counters. Login tokens, web sessions,
// pending registrations and action links are stored hashed and expire.
// The history needed by the admin and operator pages (logins, sessions,
// admin actions) is kept for a bounded time (SetRetention); IP addresses are
// stored already rendered by the server's IP mode (full, truncated, hmac).
// Nothing about the people being helped is kept beyond a truncated host ID,
// except the abuse reports they choose to send (reports.go).
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DBFile is the database file name inside RDV_DATA_DIR.
const DBFile = "accounts.db"

// Lifetimes.
const (
	TokenTTL        = 30 * 24 * time.Hour // sliding
	TokenRefresh    = 24 * time.Hour      // the expiry slides at most once per period
	VerifyTTL       = 24 * time.Hour
	InactiveTTL     = 2 * 365 * 24 * time.Hour // deleted after 24 months without login
	maxPendingEmail = 3                        // pending registrations kept per address

	// Web sessions of the account pages.
	WebIdleTTL     = 12 * time.Hour
	WebAbsoluteTTL = 7 * 24 * time.Hour

	DefaultHistoryTTL = 90 * 24 * time.Hour
	DefaultAuditTTL   = 365 * 24 * time.Hour
)

// Errors.
var (
	ErrNotFound = errors.New("accounts: not found")
	ErrExists   = errors.New("accounts: email already registered")
	// ErrBanned: the address was banned by an admin (BanAccount).
	ErrBanned = errors.New("accounts: email address banned")
)

// Roles.
const (
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

// Account is an operator account.
type Account struct {
	ID          int64
	Email       string
	Name        string
	Verified    bool // the email address was verified through a link
	Disabled    bool
	Created     time.Time
	VerifiedAt  time.Time // zero when not verified
	LastLogin   time.Time // zero before the first login
	LastLoginIP string    // rendered by the server's IP mode
	Role        string
	TOTP        bool // a TOTP secret is enrolled
	Quota       QuotaOverride
	// Review is when the account was disabled by the abuse-report
	// protection, pending an admin's review (zero otherwise).
	Review time.Time
	// Banned: disabled by an admin after reports; its address cannot
	// register again (see BanAccount).
	Banned bool
}

// IsAdmin reports the admin role.
func (a Account) IsAdmin() bool { return a.Role == RoleAdmin }

// QuotaOverride replaces the instance quotas for one account; nil keeps the
// instance value (0 = unlimited).
type QuotaOverride struct {
	MaxSessions *int64
	SessionsDay *int64
	RelayBytes  *int64
}

// Usage is the quota state of an account.
type Usage struct {
	SessionsToday   int
	RelayBytesMonth int64
}

// Store is the account database. All methods are safe for concurrent use.
type Store struct {
	db *sql.DB

	mu         sync.Mutex
	clock      func() time.Time
	closed     bool
	historyTTL time.Duration
	auditTTL   time.Duration
}

// Open opens (creates) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("accounts: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=secure_delete(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	// One connection: SQLite serialises writers anyway, and one connection
	// avoids SQLITE_BUSY between the server's own goroutines.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, clock: time.Now, historyTTL: DefaultHistoryTTL, auditTTL: DefaultAuditTTL}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%w (database %s: its directory must be writable by this process, uid %d)", err, path, os.Getuid())
	}
	_ = os.Chmod(path, 0o600)
	return s, nil
}

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	s.clock = now
	s.mu.Unlock()
}

func (s *Store) now() time.Time {
	s.mu.Lock()
	f := s.clock
	s.mu.Unlock()
	return f()
}

// Close closes the database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// migrate brings the schema to the current version. Each step runs in a
// transaction and is idempotent (columns are added only when missing), so a
// step interrupted half way is simply run again.
func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("accounts: %w", err)
	}
	if v > len(migrations) {
		return fmt.Errorf("accounts: database schema %d is newer than this binary (%d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("accounts: %w", err)
		}
		if err := migrations[i](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("accounts: schema %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("accounts: schema %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("accounts: schema %d: %w", i+1, err)
		}
	}
	return nil
}

// SchemaVersion is the schema this binary writes.
func SchemaVersion() int { return len(migrations) }

// NewSecret returns a random 256-bit opaque secret (base64url) and its
// storage hash.
func NewSecret(prefix string) (secret, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	secret = prefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, HashSecret(secret)
}

// HashSecret is the storage form of a token: SHA-256, hex. Tokens carry 256
// random bits, so a fast hash is enough (no password stretching needed).
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func unix(t time.Time) int64 { return t.UTC().Unix() }

func fromUnix(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0).UTC()
}

const accountCols = `id, email, name, pw_hash, created_at, verified_at, last_login_at, disabled,
	last_login_ip, role, totp_secret IS NOT NULL, q_max_sessions, q_sessions_day, q_relay_bytes, review_at, banned`

type scanner interface{ Scan(...any) error }

func nullPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func scanAccount(r scanner) (Account, string, error) {
	var (
		a                   Account
		pw                  string
		created             int64
		verified, lastLogin sql.NullInt64
		disabled            int
		totp                bool
		qm, qd, qr          sql.NullInt64
		review              sql.NullInt64
		banned              int
	)
	if err := r.Scan(&a.ID, &a.Email, &a.Name, &pw, &created, &verified, &lastLogin, &disabled,
		&a.LastLoginIP, &a.Role, &totp, &qm, &qd, &qr, &review, &banned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, "", ErrNotFound
		}
		return Account{}, "", err
	}
	a.Created = time.Unix(created, 0).UTC()
	a.VerifiedAt, a.LastLogin = fromUnix(verified), fromUnix(lastLogin)
	a.Verified, a.Disabled, a.TOTP = verified.Valid, disabled != 0, totp
	a.Quota = QuotaOverride{MaxSessions: nullPtr(qm), SessionsDay: nullPtr(qd), RelayBytes: nullPtr(qr)}
	a.Review, a.Banned = fromUnix(review), banned != 0
	return a, pw, nil
}

// ByEmail returns an account and its password hash.
func (s *Store) ByEmail(ctx context.Context, email string) (Account, string, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE email = ?`, email))
}

// ByID returns an account.
func (s *Store) ByID(ctx context.Context, id int64) (Account, error) {
	a, _, err := scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id = ?`, id))
	return a, err
}

// Create inserts an account (registration without email verification). A
// banned address gets ErrBanned.
func (s *Store) Create(ctx context.Context, email, name, pwHash string, verified bool) (Account, error) {
	if banned, err := s.EmailBanned(ctx, email); err != nil {
		return Account{}, err
	} else if banned {
		return Account{}, ErrBanned
	}
	now := unix(s.now())
	var ver any
	if verified {
		ver = now
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO accounts(email, name, pw_hash, created_at, verified_at) VALUES(?,?,?,?,?)
		ON CONFLICT(email) DO NOTHING`, email, name, pwHash, now, ver)
	if err != nil {
		return Account{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Account{}, ErrExists
	}
	a, _, err := s.ByEmail(ctx, email)
	return a, err
}

// AddPending records a registration waiting for its email verification and
// returns the secret of the verification link. Only the most recent
// registrations of an address are kept.
func (s *Store) AddPending(ctx context.Context, email, name, pwHash string) (string, error) {
	secret, hash := NewSecret("")
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO pending(hash, email, name, pw_hash, created_at, expires_at) VALUES(?,?,?,?,?,?)`,
		hash, email, name, pwHash, unix(now), unix(now.Add(VerifyTTL))); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending WHERE email = ? AND hash NOT IN
		(SELECT hash FROM pending WHERE email = ? ORDER BY created_at DESC, rowid DESC LIMIT ?)`, email, email, maxPendingEmail); err != nil {
		return "", err
	}
	return secret, tx.Commit()
}

// PendingValid reports whether a verification secret is valid (not used,
// not expired), without consuming it.
func (s *Store) PendingValid(ctx context.Context, secret string) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending WHERE hash = ? AND expires_at > ?`, HashSecret(secret), unix(s.now())).Scan(&n)
	return err == nil && n == 1
}

// PendingHashes returns the password hashes of the live registrations of an
// address (to tell "not verified yet" from "wrong password" at login).
func (s *Store) PendingHashes(ctx context.Context, email string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pw_hash FROM pending WHERE email = ? AND expires_at > ?`, email, unix(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Verify consumes a verification secret: the account is created, or an
// existing unverified one takes the verified registration's name and
// password. Every pending registration of the address is dropped. A
// verified, existing account is left untouched (created=false).
func (s *Store) Verify(ctx context.Context, secret string) (a Account, created bool, err error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, false, err
	}
	defer tx.Rollback()
	var email, name, pw string
	err = tx.QueryRowContext(ctx, `SELECT email, name, pw_hash FROM pending WHERE hash = ? AND expires_at > ?`,
		HashSecret(secret), unix(now)).Scan(&email, &name, &pw)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, ErrNotFound
	}
	if err != nil {
		return Account{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending WHERE email = ?`, email); err != nil {
		return Account{}, false, err
	}
	var banned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM banned_emails WHERE email = ?`, email).Scan(&banned); err != nil {
		return Account{}, false, err
	}
	if banned > 0 {
		// The address was banned after the registration: the link is void.
		if err := tx.Commit(); err != nil {
			return Account{}, false, err
		}
		return Account{}, false, ErrNotFound
	}
	cur, _, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE email = ?`, email))
	switch {
	case errors.Is(err, ErrNotFound):
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(email, name, pw_hash, created_at, verified_at) VALUES(?,?,?,?,?)`,
			email, name, pw, unix(now), unix(now)); err != nil {
			return Account{}, false, err
		}
		created = true
	case err != nil:
		return Account{}, false, err
	case !cur.Verified:
		// Proof of the mailbox: the verified registration replaces an
		// account created while verification was off, and its sessions end.
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET name = ?, pw_hash = ?, verified_at = ? WHERE id = ?`, name, pw, unix(now), cur.ID); err != nil {
			return Account{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE account_id = ?`, cur.ID); err != nil {
			return Account{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE account_id = ?`, cur.ID); err != nil {
			return Account{}, false, err
		}
		created = true
	}
	a, _, err = scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE email = ?`, email))
	if err != nil {
		return Account{}, false, err
	}
	return a, created, tx.Commit()
}

// MaxClientLen bounds the client string stored with a token.
const MaxClientLen = 64

// cleanClient keeps a short printable client string ("desktop/1.0.34").
func cleanClient(c string) string {
	b := make([]rune, 0, len(c))
	for _, r := range c {
		if r < 0x20 || r == 0x7f || r > 0x7e {
			continue
		}
		b = append(b, r)
		if len(b) == MaxClientLen {
			break
		}
	}
	return strings.TrimSpace(string(b))
}

// IssueToken creates a login token for an account and records the login.
func (s *Store) IssueToken(ctx context.Context, accountID int64) (string, time.Time, error) {
	return s.IssueTokenFor(ctx, accountID, "", "")
}

// IssueTokenFor creates a login token, remembering the client string the
// application announced and the (rendered) IP address of the login.
func (s *Store) IssueTokenFor(ctx context.Context, accountID int64, client, ip string) (string, time.Time, error) {
	secret, hash := NewSecret("rdv1.")
	now := s.now()
	exp := now.Add(TokenTTL).UTC().Truncate(time.Second)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(hash, account_id, expires_at, refreshed_at, client, created_at, last_used_at, last_ip)
		VALUES(?,?,?,?,?,?,?,?)`, hash, accountID, unix(exp), unix(now), cleanClient(client), unix(now), unix(now), ip); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET last_login_at = ?, last_login_ip = ? WHERE id = ?`, unix(now), ip, accountID); err != nil {
		return "", time.Time{}, err
	}
	return secret, exp, tx.Commit()
}

// tokenTouch is the minimum interval between two "last used" updates of a
// token.
const tokenTouch = time.Minute

// ResolveToken returns the account of a live token and slides its expiry
// (at most once per TokenRefresh). Expired tokens and tokens of disabled
// accounts resolve to ErrNotFound.
func (s *Store) ResolveToken(ctx context.Context, token string) (Account, time.Time, error) {
	return s.ResolveTokenFrom(ctx, token, "")
}

// ResolveTokenFrom is ResolveToken that also records when (at most once a
// minute) and from where (rendered IP, "" to keep the last one) the token
// was used.
func (s *Store) ResolveTokenFrom(ctx context.Context, token, ip string) (Account, time.Time, error) {
	if token == "" || len(token) > 128 {
		return Account{}, time.Time{}, ErrNotFound
	}
	now := s.now()
	hash := HashSecret(token)
	var (
		accountID, exp, refreshed, used int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT account_id, expires_at, refreshed_at, last_used_at FROM tokens WHERE hash = ?`, hash).Scan(&accountID, &exp, &refreshed, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, time.Time{}, ErrNotFound
	}
	if err != nil {
		return Account{}, time.Time{}, err
	}
	if exp <= unix(now) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM tokens WHERE hash = ?`, hash)
		return Account{}, time.Time{}, ErrNotFound
	}
	a, err := s.ByID(ctx, accountID)
	if err != nil {
		return Account{}, time.Time{}, err
	}
	if a.Disabled {
		return Account{}, time.Time{}, ErrNotFound
	}
	expires := time.Unix(exp, 0).UTC()
	if now.Sub(time.Unix(refreshed, 0)) >= TokenRefresh {
		expires = now.Add(TokenTTL).UTC().Truncate(time.Second)
		if _, err := s.db.ExecContext(ctx, `UPDATE tokens SET expires_at = ?, refreshed_at = ? WHERE hash = ?`, unix(expires), unix(now), hash); err != nil {
			return Account{}, time.Time{}, err
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE accounts SET last_login_at = ? WHERE id = ?`, unix(now), accountID)
	}
	if now.Sub(time.Unix(used, 0)) >= tokenTouch {
		_, _ = s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ?, last_ip = CASE WHEN ? = '' THEN last_ip ELSE ? END WHERE hash = ?`,
			unix(now), ip, ip, hash)
	}
	return a, expires, nil
}

// RevokeToken deletes one token.
func (s *Store) RevokeToken(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE hash = ?`, HashSecret(token))
	return err
}

// List returns every account, oldest first, with its current usage.
func (s *Store) List(ctx context.Context) ([]Account, []Usage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	var out []Account
	for rows.Next() {
		a, _, err := scanAccount(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	us := make([]Usage, len(out))
	for i, a := range out {
		if us[i], err = s.Usage(ctx, a.ID); err != nil {
			return nil, nil, err
		}
	}
	return out, us, nil
}

// SetDisabled disables (and logs out everywhere: Desktop tokens, web
// sessions, pending action links) or re-enables an account.
func (s *Store) SetDisabled(ctx context.Context, email string, disabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v := 0
	if disabled {
		v = 1
	}
	res, err := tx.ExecContext(ctx, `UPDATE accounts SET disabled = ? WHERE email = ?`, v, email)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	qs := []string{
		`DELETE FROM tokens WHERE account_id = (SELECT id FROM accounts WHERE email = ?)`,
		`DELETE FROM web_sessions WHERE account_id = (SELECT id FROM accounts WHERE email = ?)`,
		`DELETE FROM action_tokens WHERE account_id = (SELECT id FROM accounts WHERE email = ?)`,
	}
	if !disabled {
		// Enabling ends a review and a ban (the address may register again).
		qs = []string{
			`UPDATE accounts SET review_at = NULL, banned = 0 WHERE email = ?`,
			`DELETE FROM banned_emails WHERE email = ?`,
		}
	}
	for _, q := range qs {
		if _, err := tx.ExecContext(ctx, q, email); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete removes an account, its tokens and counters, and its pending
// registrations.
func (s *Store) Delete(ctx context.Context, email string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE email = ?`, email)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	res2, err := tx.ExecContext(ctx, `DELETE FROM pending WHERE email = ?`, email)
	if err != nil {
		return err
	}
	m, _ := res2.RowsAffected()
	if n+m == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func day(t time.Time) string   { return t.UTC().Format("2006-01-02") }
func month(t time.Time) string { return t.UTC().Format("2006-01") }

func firstOfMonth(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Usage returns today's sessions and this month's relayed bytes (UTC).
func (s *Store) Usage(ctx context.Context, accountID int64) (Usage, error) {
	now := s.now()
	var u Usage
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT sessions FROM usage_day WHERE account_id = ? AND day = ?), 0),
		COALESCE((SELECT relay_bytes FROM usage_month WHERE account_id = ? AND month = ?), 0)`,
		accountID, day(now), accountID, month(now)).Scan(&u.SessionsToday, &u.RelayBytesMonth)
	return u, err
}

// AddSession counts one session for today. Quotas are enforced by the
// server from Usage plus its own in-memory count (one process owns the
// counters).
func (s *Store) AddSession(ctx context.Context, accountID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage_day(account_id, day, sessions) VALUES(?,?,1)
		ON CONFLICT(account_id, day) DO UPDATE SET sessions = sessions + 1`, accountID, day(s.now()))
	return err
}

// AddRelay adds relayed bytes to this month's counter.
func (s *Store) AddRelay(ctx context.Context, accountID int64, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage_month(account_id, month, relay_bytes) VALUES(?,?,?)
		ON CONFLICT(account_id, month) DO UPDATE SET relay_bytes = relay_bytes + excluded.relay_bytes`, accountID, month(s.now()), bytes)
	return err
}

// Purge deletes what is no longer needed: expired tokens, web sessions,
// registrations and action links, counters older than the previous month,
// history older than the retention, and accounts inactive for InactiveTTL
// (data minimisation). It returns the number of deleted accounts.
func (s *Store) Purge(ctx context.Context) (int64, error) {
	now := s.now()
	s.mu.Lock()
	hist, audit := s.historyTTL, s.auditTTL
	s.mu.Unlock()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM tokens WHERE expires_at <= ?`, []any{unix(now)}},
		{`DELETE FROM pending WHERE expires_at <= ?`, []any{unix(now)}},
		{`DELETE FROM action_tokens WHERE expires_at <= ?`, []any{unix(now)}},
		{`DELETE FROM web_sessions WHERE created_at <= ? OR last_seen_at <= ?`, []any{unix(now.Add(-WebAbsoluteTTL)), unix(now.Add(-WebIdleTTL))}},
		{`DELETE FROM usage_day WHERE day < ?`, []any{day(now.AddDate(0, 0, -2))}},
		{`DELETE FROM usage_month WHERE month < ?`, []any{month(firstOfMonth(now).AddDate(0, -1, 0))}},
		// Abuse reports: a closed report follows the history
		// retention from its closing; an open one is kept until an admin
		// closes it, and so is the history row of its session.
		{`DELETE FROM reports WHERE status <> 'open' AND COALESCE(closed_at, created_at) < ?`, []any{unix(now.Add(-hist))}},
		{`DELETE FROM session_history WHERE ended_at < ? AND NOT (sid <> '' AND EXISTS
			(SELECT 1 FROM reports r WHERE r.sid = session_history.sid AND r.status = 'open'))`, []any{unix(now.Add(-hist))}},
		{`DELETE FROM login_history WHERE at < ?`, []any{unix(now.Add(-hist))}},
		// A ban outlives a deleted account for InactiveTTL.
		{`DELETE FROM banned_emails WHERE at < ? AND email NOT IN (SELECT email FROM accounts)`, []any{unix(now.Add(-InactiveTTL))}},
		{`DELETE FROM audit_log WHERE at < ?`, []any{unix(now.Add(-audit))}},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.args...); err != nil {
			return 0, err
		}
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE COALESCE(last_login_at, created_at) < ?`, unix(now.Add(-InactiveTTL)))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// NormalizeEmail lowercases and trims an address.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
