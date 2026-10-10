package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Abuse reports: the person helped reports the operator
// of a session it accepted. A report holds the session id, the operator
// (account, or static key label, and the identity shown to the host at the
// time), the truncated host ID, a reason, an optional text written by the
// reporter, the time, the reporter's address rendered by the server's IP
// mode and a keyed pseudonym of it (only used to count distinct reporters).
// Never any session content: the server has none.

// Report statuses.
const (
	ReportOpen     = "open"
	ReportHandled  = "handled"   // closed: abuse confirmed and dealt with
	ReportNotAbuse = "not_abuse" // closed: no abuse
)

// MaxNoteLen bounds an admin note on a report (characters).
const MaxNoteLen = 2000

// ReportRecord is one abuse report.
type ReportRecord struct {
	ID          int64
	SID         string
	AccountID   int64  // 0 for a static key (or a deleted account)
	KeyLabel    string // static key label
	Operator    string // identity shown to the host at the time
	Email       string // current address of the account (read side)
	HostTID     string
	Reason      string
	Text        string
	Created     time.Time
	ReporterIP  string // rendered by the server's IP mode
	ReporterKey string // keyed pseudonym of the reporter's address (distinct counting only)
	Started     time.Time
	Accepted    time.Time
	Ended       time.Time // zero when reported during the session (until it ends)
	Path        string
	Status      string
	Closed      time.Time
	ClosedBy    string
	Notes       []ReportNote // Report only
}

// ReportNote is an admin note on a report.
type ReportNote struct {
	At       time.Time
	AuthorID int64
	Author   string
	Text     string
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return unix(t)
}

// AddReport stores a report. A second report of the same session is
// refused with ErrExists.
func (s *Store) AddReport(ctx context.Context, r ReportRecord) (int64, error) {
	var acct any
	if r.AccountID != 0 {
		acct = r.AccountID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO reports(sid, account_id, key_label, operator, host_tid, reason, text, created_at,
		reporter_ip, reporter_key, session_started, session_accepted, session_ended, path, status)
		SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,'open' WHERE ? IS NULL OR EXISTS (SELECT 1 FROM accounts WHERE id = ?)
		ON CONFLICT(sid) DO NOTHING`,
		r.SID, acct, r.KeyLabel, r.Operator, r.HostTID, r.Reason, r.Text, unix(s.now()), r.ReporterIP, r.ReporterKey,
		nullTime(r.Started), nullTime(r.Accepted), nullTime(r.Ended), r.Path, acct, acct)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reports WHERE sid = ?`, r.SID).Scan(&exists); err != nil {
			return 0, err
		}
		if exists > 0 {
			return 0, ErrExists
		}
		return 0, ErrNotFound // the account was deleted meanwhile
	}
	return res.LastInsertId()
}

// SetReportSessionEnd records the end of a session reported while it ran.
func (s *Store) SetReportSessionEnd(ctx context.Context, sid string, ended time.Time, path string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE reports SET session_ended = ?, path = CASE WHEN ? = '' THEN path ELSE ? END
		WHERE sid = ? AND session_ended IS NULL`, unix(ended), path, path, sid)
	return err
}

const reportCols = `r.id, r.sid, COALESCE(r.account_id, 0), r.key_label, r.operator, COALESCE(a.email, ''), r.host_tid, r.reason, r.text,
	r.created_at, r.reporter_ip, r.reporter_key, r.session_started, r.session_accepted, r.session_ended, r.path, r.status, r.closed_at, r.closed_by`

func scanReport(sc scanner) (ReportRecord, error) {
	var (
		r                 ReportRecord
		created           int64
		st, acc, en, clos sql.NullInt64
	)
	if err := sc.Scan(&r.ID, &r.SID, &r.AccountID, &r.KeyLabel, &r.Operator, &r.Email, &r.HostTID, &r.Reason, &r.Text,
		&created, &r.ReporterIP, &r.ReporterKey, &st, &acc, &en, &r.Path, &r.Status, &clos, &r.ClosedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReportRecord{}, ErrNotFound
		}
		return ReportRecord{}, err
	}
	r.Created = time.Unix(created, 0).UTC()
	r.Started, r.Accepted, r.Ended, r.Closed = fromUnix(st), fromUnix(acc), fromUnix(en), fromUnix(clos)
	return r, nil
}

// Report returns one report with its notes (oldest first).
func (s *Store) Report(ctx context.Context, id int64) (ReportRecord, error) {
	r, err := scanReport(s.db.QueryRowContext(ctx, `SELECT `+reportCols+` FROM reports r LEFT JOIN accounts a ON a.id = r.account_id WHERE r.id = ?`, id))
	if err != nil {
		return r, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at, author_id, author, text FROM report_notes WHERE report_id = ? ORDER BY at, id`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var n ReportNote
		var at int64
		if err := rows.Scan(&at, &n.AuthorID, &n.Author, &n.Text); err != nil {
			return r, err
		}
		n.At = time.Unix(at, 0).UTC()
		r.Notes = append(r.Notes, n)
	}
	return r, rows.Err()
}

// ReportFilter selects reports: Status "open", "closed" (handled or not
// abuse) or "" (all); AccountID > 0 keeps one operator account; KeyLabel a
// static key (with AccountID 0).
type ReportFilter struct {
	Status    string
	AccountID int64
	KeyLabel  string
}

// Reports lists reports, most recent first, with the total of matches.
func (s *Store) Reports(ctx context.Context, f ReportFilter, limit, offset int) ([]ReportRecord, int, error) {
	var where []string
	var args []any
	switch f.Status {
	case ReportOpen:
		where = append(where, `r.status = 'open'`)
	case "closed":
		where = append(where, `r.status <> 'open'`)
	}
	switch {
	case f.AccountID > 0:
		where = append(where, `r.account_id = ?`)
		args = append(args, f.AccountID)
	case f.KeyLabel != "":
		where = append(where, `r.account_id IS NULL AND r.key_label = ?`)
		args = append(args, f.KeyLabel)
	}
	w := ""
	if len(where) > 0 {
		w = ` WHERE ` + strings.Join(where, " AND ")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reports r`+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+reportCols+` FROM reports r LEFT JOIN accounts a ON a.id = r.account_id`+w+
		` ORDER BY r.created_at DESC, r.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []ReportRecord
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// SetReportStatus closes (handled, not_abuse) or reopens (open) a report.
func (s *Store) SetReportStatus(ctx context.Context, id int64, status, by string) error {
	switch status {
	case ReportOpen:
		return s.exec1(ctx, `UPDATE reports SET status = 'open', closed_at = NULL, closed_by = '' WHERE id = ?`, id)
	case ReportHandled, ReportNotAbuse:
		return s.exec1(ctx, `UPDATE reports SET status = ?, closed_at = ?, closed_by = ? WHERE id = ?`, status, unix(s.now()), by, id)
	}
	return errors.New("accounts: invalid report status")
}

// AddReportNote appends an admin note to a report.
func (s *Store) AddReportNote(ctx context.Context, id, authorID int64, author, text string) error {
	if r := []rune(text); len(r) > MaxNoteLen {
		text = string(r[:MaxNoteLen])
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO report_notes(report_id, at, author_id, author, text)
		SELECT ?,?,?,?,? WHERE EXISTS (SELECT 1 FROM reports WHERE id = ?)`, id, unix(s.now()), authorID, author, text, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReportCounts returns the open reports and the reports received since a
// time (admin dashboard).
func (s *Store) ReportCounts(ctx context.Context, since time.Time) (open, recent int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(status = 'open'), 0), COALESCE(SUM(created_at >= ?), 0) FROM reports`,
		unix(since)).Scan(&open, &recent)
	return
}

// OpenReportCount counts the open reports about one account.
func (s *Store) OpenReportCount(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reports WHERE account_id = ? AND status = 'open'`, accountID).Scan(&n)
	return n, err
}

// DistinctReporters counts the distinct reporters (by their keyed
// pseudonym) of an account since a time, ignoring the reports an admin
// closed as not abuse (auto-protection threshold).
func (s *Store) DistinctReporters(ctx context.Context, accountID int64, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT CASE WHEN reporter_key <> '' THEN reporter_key ELSE 'sid:' || sid END)
		FROM reports WHERE account_id = ? AND created_at >= ? AND status <> 'not_abuse'`, accountID, unix(since)).Scan(&n)
	return n, err
}

// ReportableSession is what the history keeps to accept a report after the
// session ended (the server lost its memory of it, e.g. after a restart).
type ReportableSession struct {
	SessionRecord
	ReportHash string
}

// SessionBySID returns the history row of an ended session by its id.
func (s *Store) SessionBySID(ctx context.Context, sid string) (ReportableSession, error) {
	if sid == "" {
		return ReportableSession{}, ErrNotFound
	}
	var (
		r       ReportableSession
		st, en  int64
		acc     sql.NullInt64
		account sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, account_id, key_label, host_tid, started_at, accepted_at, ended_at, path, relay_bytes,
		end_reason, sid, report_hash FROM session_history WHERE sid = ? ORDER BY id DESC LIMIT 1`, sid).
		Scan(&r.ID, &account, &r.KeyLabel, &r.HostTID, &st, &acc, &en, &r.Path, &r.Relayed, &r.EndReason, &r.SID, &r.ReportHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ReportableSession{}, ErrNotFound
	}
	if err != nil {
		return ReportableSession{}, err
	}
	r.AccountID = account.Int64
	r.Started, r.Accepted, r.Ended = time.Unix(st, 0).UTC(), fromUnix(acc), time.Unix(en, 0).UTC()
	return r, nil
}

// SetReview marks an account as disabled pending review (on) or clears the
// mark. The account must be disabled separately (SetDisabledID).
func (s *Store) SetReview(ctx context.Context, id int64, on bool) error {
	var v any
	if on {
		v = unix(s.now())
	}
	return s.exec1(ctx, `UPDATE accounts SET review_at = ? WHERE id = ?`, v, id)
}

// BanAccount disables an account, ends all its sign-ins, and bans its
// address: it cannot register again (nor be given to another account)
// until an admin enables the account (SetDisabled false) or the ban
// expires with the account (InactiveTTL after its deletion). The last
// enabled admin cannot be banned (ErrLastAdmin).
func (s *Store) BanAccount(ctx context.Context, id int64, by, reason string) error {
	a, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.SetDisabledID(ctx, id, true); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET banned = 1 WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO banned_emails(email, at, by, reason) VALUES(?,?,?,?)
		ON CONFLICT(email) DO UPDATE SET at = excluded.at, by = excluded.by, reason = excluded.reason`, a.Email, unix(s.now()), by, reason); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending WHERE email = ?`, a.Email); err != nil {
		return err
	}
	return tx.Commit()
}

// EmailBanned reports whether an address is banned.
func (s *Store) EmailBanned(ctx context.Context, email string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM banned_emails WHERE email = ?`, NormalizeEmail(email)).Scan(&n)
	return n > 0, err
}
