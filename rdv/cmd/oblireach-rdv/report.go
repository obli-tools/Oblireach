package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
)

// reportCmd lists and closes the abuse reports of the local instance (the
// console has the full view):
//
//	docker compose exec rdv /oblireach-rdv report list [open|closed|all]
//	docker compose exec rdv /oblireach-rdv report close <id> handled|not_abuse
func reportCmd(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: oblireach-rdv report list [open|closed|all] | close <id> handled|not_abuse")
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
	switch args[0] {
	case "list":
		status := accounts.ReportOpen
		if len(args) > 2 {
			return usage()
		}
		if len(args) == 2 {
			status = args[1]
		}
		switch status {
		case accounts.ReportOpen, "closed":
		case "all":
			status = ""
		default:
			return usage()
		}
		list, total, err := st.Reports(ctx, accounts.ReportFilter{Status: status}, 1000, 0)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tCREATED\tSTATUS\tREASON\tOPERATOR\tHOST\tSESSION\tTEXT")
		for _, r := range list {
			who := r.Email
			if r.AccountID == 0 {
				who = "key:" + r.KeyLabel
			}
			text := []rune(r.Text)
			if len(text) > 40 {
				text = append(text[:40], []rune("...")...)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%q\n", r.ID, date(r.Created), r.Status, r.Reason, who, r.HostTID,
				wire.TruncSID(r.SID), string(text))
		}
		_ = tw.Flush()
		fmt.Fprintf(stdout, "%d report(s)\n", total)
		return 0
	case "close":
		if len(args) != 3 {
			return usage()
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || (args[2] != accounts.ReportHandled && args[2] != accounts.ReportNotAbuse) {
			return usage()
		}
		r, err := st.Report(ctx, id)
		if err == nil {
			err = st.SetReportStatus(ctx, id, args[2], "cli")
		}
		if errors.Is(err, accounts.ErrNotFound) {
			fmt.Fprintf(stderr, "no report %d\n", id)
			return 1
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = st.Audit(ctx, accounts.AuditEntry{Actor: "cli", Action: "report_" + args[2], TargetID: r.AccountID, Target: r.Email,
			Detail: "report=" + args[1]})
		fmt.Fprintf(stdout, "report %d: closed (%s)\n", id, args[2])
		return 0
	}
	return usage()
}
