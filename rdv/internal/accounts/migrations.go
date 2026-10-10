package accounts

import (
	"database/sql"
	"strings"
)

// migrations[i] brings the schema from version i to i+1.
var migrations = []func(*sql.Tx) error{
	migrate1,
	migrate2,
	migrate3,
}

func execAll(tx *sql.Tx, stmts ...string) error {
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// addColumn adds a column unless it exists (idempotent ALTER TABLE).
func addColumn(tx *sql.Tx, table, col, def string) error {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		if strings.EqualFold(n, col) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + def)
	return err
}

// migrate1 is the initial schema (accounts, tokens, pending registrations,
// usage counters).
func migrate1(tx *sql.Tx) error {
	return execAll(tx, `
CREATE TABLE IF NOT EXISTS accounts(
  id            INTEGER PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  pw_hash       TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  verified_at   INTEGER,
  last_login_at INTEGER,
  disabled      INTEGER NOT NULL DEFAULT 0
)`, `
CREATE TABLE IF NOT EXISTS tokens(
  hash         TEXT PRIMARY KEY,
  account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  expires_at   INTEGER NOT NULL,
  refreshed_at INTEGER NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS tokens_account ON tokens(account_id)`, `
CREATE TABLE IF NOT EXISTS pending(
  hash       TEXT PRIMARY KEY,
  email      TEXT NOT NULL,
  name       TEXT NOT NULL,
  pw_hash    TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS pending_email ON pending(email)`, `
CREATE TABLE IF NOT EXISTS usage_day(
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  day        TEXT NOT NULL,
  sessions   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(account_id, day)
)`, `
CREATE TABLE IF NOT EXISTS usage_month(
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  month       TEXT NOT NULL,
  relay_bytes INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(account_id, month)
)`)
}

// migrate2 adds roles, quota overrides, TOTP, token details, web sessions,
// login / session history, the audit log and action links.
func migrate2(tx *sql.Tx) error {
	for _, c := range [][3]string{
		{"accounts", "role", "TEXT NOT NULL DEFAULT 'operator'"},
		{"accounts", "last_login_ip", "TEXT NOT NULL DEFAULT ''"},
		{"accounts", "totp_secret", "BLOB"},
		{"accounts", "totp_last_step", "INTEGER NOT NULL DEFAULT 0"},
		{"accounts", "q_max_sessions", "INTEGER"},
		{"accounts", "q_sessions_day", "INTEGER"},
		{"accounts", "q_relay_bytes", "INTEGER"},
		{"tokens", "client", "TEXT NOT NULL DEFAULT ''"},
		{"tokens", "created_at", "INTEGER NOT NULL DEFAULT 0"},
		{"tokens", "last_used_at", "INTEGER NOT NULL DEFAULT 0"},
		{"tokens", "last_ip", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumn(tx, c[0], c[1], c[2]); err != nil {
			return err
		}
	}
	return execAll(tx,
		`UPDATE tokens SET created_at = refreshed_at WHERE created_at = 0`,
		`UPDATE tokens SET last_used_at = refreshed_at WHERE last_used_at = 0`, `
CREATE TABLE IF NOT EXISTS web_sessions(
  hash         TEXT PRIMARY KEY,
  account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip           TEXT NOT NULL DEFAULT '',
  mfa          INTEGER NOT NULL DEFAULT 0
)`,
		`CREATE INDEX IF NOT EXISTS web_sessions_account ON web_sessions(account_id)`, `
CREATE TABLE IF NOT EXISTS login_history(
  id         INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  at         INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  ok         INTEGER NOT NULL,
  reason     TEXT NOT NULL DEFAULT '',
  ip         TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS login_history_account ON login_history(account_id, at)`,
		`CREATE INDEX IF NOT EXISTS login_history_at ON login_history(at)`, `
CREATE TABLE IF NOT EXISTS session_history(
  id          INTEGER PRIMARY KEY,
  account_id  INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  key_label   TEXT NOT NULL DEFAULT '',
  host_tid    TEXT NOT NULL,
  started_at  INTEGER NOT NULL,
  accepted_at INTEGER,
  ended_at    INTEGER NOT NULL,
  path        TEXT NOT NULL,
  relay_bytes INTEGER NOT NULL DEFAULT 0,
  end_reason  TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS session_history_ended ON session_history(ended_at)`,
		`CREATE INDEX IF NOT EXISTS session_history_account ON session_history(account_id, ended_at)`, `
CREATE TABLE IF NOT EXISTS audit_log(
  id        INTEGER PRIMARY KEY,
  at        INTEGER NOT NULL,
  actor_id  INTEGER NOT NULL DEFAULT 0,
  actor     TEXT NOT NULL,
  action    TEXT NOT NULL,
  target_id INTEGER NOT NULL DEFAULT 0,
  target    TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT '',
  ip        TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS audit_log_at ON audit_log(at)`,
		// Append-only: rows are never edited (the retention purge deletes
		// the oldest ones).
		`CREATE TRIGGER IF NOT EXISTS audit_log_append_only BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit log is append-only'); END`, `
CREATE TABLE IF NOT EXISTS action_tokens(
  hash       TEXT PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  new_email  TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS action_tokens_account ON action_tokens(account_id)`)
}

// migrate3 adds abuse reports: the reports and their notes, the session id
// and report-token hash of the session history (a report is bound to a real
// session), the review and ban state of accounts, and the banned addresses.
func migrate3(tx *sql.Tx) error {
	for _, c := range [][3]string{
		{"accounts", "review_at", "INTEGER"},
		{"accounts", "banned", "INTEGER NOT NULL DEFAULT 0"},
		{"session_history", "sid", "TEXT NOT NULL DEFAULT ''"},
		{"session_history", "report_hash", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumn(tx, c[0], c[1], c[2]); err != nil {
			return err
		}
	}
	return execAll(tx,
		`CREATE INDEX IF NOT EXISTS session_history_sid ON session_history(sid)`, `
CREATE TABLE IF NOT EXISTS reports(
  id               INTEGER PRIMARY KEY,
  sid              TEXT NOT NULL UNIQUE,
  account_id       INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
  key_label        TEXT NOT NULL DEFAULT '',
  operator         TEXT NOT NULL DEFAULT '',
  host_tid         TEXT NOT NULL,
  reason           TEXT NOT NULL,
  text             TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  reporter_ip      TEXT NOT NULL DEFAULT '',
  reporter_key     TEXT NOT NULL DEFAULT '',
  session_started  INTEGER,
  session_accepted INTEGER,
  session_ended    INTEGER,
  path             TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT 'open',
  closed_at        INTEGER,
  closed_by        TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS reports_account ON reports(account_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS reports_status ON reports(status, created_at)`, `
CREATE TABLE IF NOT EXISTS report_notes(
  id        INTEGER PRIMARY KEY,
  report_id INTEGER NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  at        INTEGER NOT NULL,
  author_id INTEGER NOT NULL DEFAULT 0,
  author    TEXT NOT NULL,
  text      TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS report_notes_report ON report_notes(report_id)`, `
CREATE TABLE IF NOT EXISTS banned_emails(
  email  TEXT PRIMARY KEY,
  at     INTEGER NOT NULL,
  by     TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT ''
)`)
}
