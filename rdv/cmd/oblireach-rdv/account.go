package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/oblireach/rdv/internal/accounts"
)

// accountCmd administers the operator accounts of the local instance. It
// opens the database directly (SQLite handles the running server as a
// concurrent writer), so it works inside the container:
//
//	docker compose exec rdv /oblireach-rdv account list
func accountCmd(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: oblireach-rdv account list | disable <email> | enable <email> | delete <email> | promote <email> | demote <email> | reset-2fa <email>")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	st, code := openStore(stderr, false)
	if st == nil {
		return code
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error

	switch args[0] {
	case "promote", "demote", "reset-2fa":
		if len(args) != 2 {
			return usage()
		}
		email := accounts.NormalizeEmail(args[1])
		a, _, err := st.ByEmail(ctx, email)
		if errors.Is(err, accounts.ErrNotFound) {
			fmt.Fprintf(stderr, "no account %s\n", email)
			return 1
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		action, done := "cli_"+args[0], args[0]+"d"
		switch args[0] {
		case "promote":
			err = st.SetRole(ctx, a.ID, accounts.RoleAdmin)
		case "demote":
			err = st.SetRole(ctx, a.ID, accounts.RoleOperator)
		default:
			action, done = "cli_reset_2fa", "two-step verification removed"
			if err = st.SetTOTP(ctx, a.ID, nil); err == nil {
				_, err = st.DeleteWebSessions(ctx, a.ID, "")
			}
		}
		if errors.Is(err, accounts.ErrLastAdmin) {
			fmt.Fprintf(stderr, "%s is the last enabled admin: promote another account first\n", email)
			return 1
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		cliAudit(ctx, st, action, a)
		fmt.Fprintf(stdout, "%s: %s\n", email, done)
		if args[0] == "demote" && listedAdmin(email) {
			fmt.Fprintln(stdout, "note: this address is in RDV_ADMIN_EMAILS; the server makes it admin again at its next check: remove it there")
		}
		return 0
	case "list":
		if len(args) != 1 {
			return usage()
		}
		list, usages, err := st.List(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tEMAIL\tNAME\tROLE\tVERIFIED\tDISABLED\t2FA\tCREATED\tLAST LOGIN\tSESSIONS TODAY\tRELAY MONTH")
		for i, a := range list {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", a.ID, a.Email, a.Name, a.Role, yesNo(a.Verified), yesNo(a.Disabled),
				yesNo(a.TOTP), date(a.Created), date(a.LastLogin), usages[i].SessionsToday, size(usages[i].RelayBytesMonth))
		}
		_ = tw.Flush()
		fmt.Fprintf(stdout, "%d account(s)\n", len(list))
		return 0
	case "disable", "enable", "delete":
		if len(args) != 2 {
			return usage()
		}
		email := accounts.NormalizeEmail(args[1])
		switch args[0] {
		case "disable":
			err = st.SetDisabled(ctx, email, true)
		case "enable":
			err = st.SetDisabled(ctx, email, false)
		default:
			err = st.Delete(ctx, email)
		}
		if errors.Is(err, accounts.ErrNotFound) {
			fmt.Fprintf(stderr, "no account %s\n", email)
			return 1
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: %sd\n", email, args[0])
		return 0
	}
	return usage()
}

// openStore opens the account database of RDV_DATA_DIR (create: also when
// it does not exist yet).
func openStore(stderr io.Writer, create bool) (*accounts.Store, int) {
	dir := os.Getenv("RDV_DATA_DIR")
	if dir == "" {
		dir = "/data"
	}
	path := filepath.Join(dir, accounts.DBFile)
	if _, err := os.Stat(path); err != nil && !create {
		fmt.Fprintf(stderr, "no account database at %s (RDV_ACCOUNTS off, or RDV_DATA_DIR differs)\n", path)
		return nil, 1
	}
	st, err := accounts.Open(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	return st, 0
}

func cliAudit(ctx context.Context, st *accounts.Store, action string, a accounts.Account) {
	_ = st.Audit(ctx, accounts.AuditEntry{Actor: "cli", Action: action, TargetID: a.ID, Target: a.Email})
}

func listedAdmin(email string) bool {
	for _, e := range strings.FieldsFunc(os.Getenv("RDV_ADMIN_EMAILS"), func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		if accounts.NormalizeEmail(e) == email {
			return true
		}
	}
	return false
}

// publicBase is the base URL of the links printed by the CLI.
func publicBase() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("RDV_PUBLIC_URL")), "/"); u != "" {
		return u
	}
	if d := strings.TrimSpace(os.Getenv("RDV_DOMAIN")); d != "" {
		return "https://" + d
	}
	return ""
}

// adminCmd creates (or upgrades) an admin account for the first set-up of
// an instance without SMTP: the account is verified, made admin, and a
// one-time link to set its password (valid 1 hour) is printed.
//
//	docker compose exec rdv /oblireach-rdv admin create <email> [display name]
func adminCmd(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: oblireach-rdv admin create <email> [display name]")
		return 2
	}
	if len(args) < 2 || args[0] != "create" {
		return usage()
	}
	email := accounts.NormalizeEmail(args[1])
	if !accounts.ValidEmail(email) {
		fmt.Fprintf(stderr, "%q is not a valid email address\n", args[1])
		return 1
	}
	name := strings.Join(args[2:], " ")
	if name == "" {
		name = "Administrator"
	}
	name, ok := accounts.CleanDisplayName(name)
	if !ok {
		fmt.Fprintln(stderr, "invalid display name (1 to 40 characters, without < > @ brackets or quotes)")
		return 1
	}
	st, code := openStore(stderr, true)
	if st == nil {
		return code
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The password is set through the link: until then the account has a
	// random one nobody knows.
	rnd := make([]byte, 32)
	if _, err := rand.Read(rnd); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	a, err := st.Create(ctx, email, name, accounts.HashPassword(hex.EncodeToString(rnd)), true)
	existed := errors.Is(err, accounts.ErrExists)
	if existed {
		a, _, err = st.ByEmail(ctx, email)
		if err == nil {
			err = st.SetVerified(ctx, a.ID)
		}
		if err == nil && a.Disabled {
			err = st.SetDisabled(ctx, email, false)
		}
	}
	if err == nil {
		err = st.SetRole(ctx, a.ID, accounts.RoleAdmin)
	}
	var secret string
	if err == nil {
		secret, err = st.NewActionToken(ctx, a.ID, accounts.ActionReset, "", time.Hour)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cliAudit(ctx, st, "cli_admin_create", a)
	base := publicBase()
	link := base + "/reset?token=" + secret
	if existed {
		fmt.Fprintf(stdout, "%s already had an account: it is now a verified admin.\n", email)
	} else {
		fmt.Fprintf(stdout, "Admin account %s created (verified).\n", email)
	}
	fmt.Fprintf(stdout, "Set its password within 1 hour (single use) by opening:\n\n  %s\n\n", link)
	if base == "" {
		fmt.Fprintln(stdout, "(RDV_PUBLIC_URL is not set: prefix the link with the public address of this server)")
	}
	fmt.Fprintf(stdout, "Then sign in at %s/login: two-step verification is set up at the first sign-in.\n", base)
	return 0
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func date(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func size(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%d B", n)
}
