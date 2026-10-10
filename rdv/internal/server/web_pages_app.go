package server

import (
	"strings"
)

// Templates of the sign-in, operator and admin pages. Same rules as the
// registration pages: no script, no style attribute (the CSP allows the
// stylesheet by hash only), charts and meters are inline SVG.

func init() {
	pageFuncs["args"] = func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	pageFuncs["isRelay"] = func(p string) bool { return strings.HasPrefix(p, "relay") }
	pageFuncs["div1e9"] = func(n int64) int64 { return n / 1_000_000_000 }
	// excerpt cuts a text to n characters on one line.
	pageFuncs["excerpt"] = func(s string, n int) string {
		s = strings.Join(strings.Fields(s), " ")
		if r := []rune(s); len(r) > n {
			return string(r[:n]) + "..."
		}
		return s
	}
	for k, v := range appBodies {
		pageBodies[k] = v
	}
}

// partials shared by the pages (parsed with the layout).
const partialsTmpl = `
{{define "csrf"}}<input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="ts" value="{{.TS}}">{{end}}
{{define "formerr"}}{{if .FormErr}}<div class="formerr" role="alert">{{.FormErr}}</div>{{end}}{{end}}
{{define "path"}}{{if eq .P "direct"}}<span class="pill blue">{{index .T "path.direct"}}</span>{{else if isRelay .P}}<span class="pill red">{{tk .T "path." .P}}</span>{{else}}<span class="pill">{{tk .T "path." (or .P "unknown")}}</span>{{end}}{{end}}
{{define "sesstable"}}<div class="tbl"><table><thead><tr>{{if .Who}}<th>{{index .T "col.operator"}}</th>{{end}}<th>{{index .T "col.start"}}</th><th>{{index .T "col.end"}}</th><th>{{index .T "col.duration"}}</th><th>{{index .T "col.host"}}</th><th>{{index .T "col.path"}}</th><th>{{index .T "col.relayed"}}</th><th>{{index .T "col.reason"}}</th></tr></thead><tbody>
{{range .Rows}}<tr>{{if $.Who}}<td>{{if .AccountID}}<a href="/admin/accounts/{{.AccountID}}">{{.Email}}</a>{{else}}<span class="pill">{{index $.T "key"}}</span> {{.KeyLabel}}{{end}}</td>{{end}}<td>{{dt .Started}}</td><td>{{dt .Ended}}</td><td>{{if .Accepted.IsZero}}<span class="muted">-</span>{{else}}{{dur .Duration}}{{end}}</td><td><code>{{.HostTID}}</code></td><td>{{if .Accepted.IsZero}}<span class="muted">-</span>{{else}}{{template "path" (args "T" $.T "P" .Path)}}{{end}}</td><td>{{bytes .Relayed}}</td><td>{{if and (eq .EndReason "reported") (not $.Adm)}}{{index $.T "reason.bye"}}{{else if or (eq .EndReason "bad_code") (eq .EndReason "admin_terminated") (eq .EndReason "quota") (eq .EndReason "reported")}}<span class="pill bad">{{tk $.T "reason." .EndReason}}</span>{{else}}{{tk $.T "reason." .EndReason}}{{end}}{{if $.Adm}}{{if .ReportID}} <a class="pill red" href="/admin/reports/{{.ReportID}}">{{index $.T "reported"}}</a>{{end}}{{end}}</td></tr>
{{else}}<tr><td colspan="8" class="muted">{{index $.T "none"}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{define "livetable"}}<div class="tbl"><table><thead><tr>{{if .Who}}<th>{{index .T "col.operator"}}</th>{{end}}<th>{{index .T "col.host"}}</th><th>{{index .T "col.state"}}</th><th>{{index .T "col.path"}}</th><th>{{index .T "col.since"}}</th><th>{{index .T "col.duration"}}</th><th>{{index .T "col.relayed"}}</th>{{if .Act}}<th></th>{{end}}</tr></thead><tbody>
{{range .Rows}}<tr>{{if $.Who}}<td>{{if .AccountID}}<a href="/admin/accounts/{{.AccountID}}">{{.Email}}</a>{{else}}<span class="pill">{{index $.T "key"}}</span> {{.KeyLabel}}{{end}}</td>{{end}}<td><code>{{.HostTID}}</code></td><td>{{tk $.T "state." .State}}</td><td>{{if eq .State "accepted"}}{{template "path" (args "T" $.T "P" .Path)}}{{else}}<span class="muted">-</span>{{end}}</td><td>{{dt .Since}}</td><td>{{dur (.Duration $.Now)}}</td><td>{{bytes .Relayed}}</td>{{if $.Act}}<td><form method="post" action="/admin/live">{{template "csrf" $.D}}<input type="hidden" name="handle" value="{{.Handle}}"><button type="submit" class="sm dark">{{index $.T "op.terminate_one"}}</button></form></td>{{end}}</tr>
{{else}}<tr><td colspan="8" class="muted">{{index $.T "none.live"}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{define "legend"}}<div class="legend"><span><i class="dot d"></i>{{index .T "path.direct"}}</span><span><i class="dot r"></i>{{index .T "path.relay"}}</span><span><i class="dot o"></i>{{index .T "path.unknown"}}</span></div>{{end}}
{{define "reporttable"}}<div class="tbl"><table><thead><tr><th>#</th><th>{{index .T "col.time"}}</th>{{if .Who}}<th>{{index .T "col.operator"}}</th>{{end}}<th>{{index .T "col.host"}}</th><th>{{index .T "col.rreason"}}</th><th>{{index .T "col.rtext"}}</th><th>{{index .T "col.status"}}</th></tr></thead><tbody>
{{range .Rows}}<tr><td><a href="/admin/reports/{{.ID}}">#{{.ID}}</a></td><td>{{dt .Created}}</td>{{if $.Who}}<td>{{if .AccountID}}<a href="/admin/accounts/{{.AccountID}}">{{or .Email .Operator}}</a>{{else}}<span class="pill">{{index $.T "key"}}</span> {{.KeyLabel}}{{end}}</td>{{end}}<td><code>{{.HostTID}}</code></td><td>{{template "rreason" (args "T" $.T "R" .Reason)}}</td><td class="wrap">{{if .Text}}{{excerpt .Text 90}}{{else}}<span class="muted">-</span>{{end}}</td><td>{{template "rstatus" (args "T" $.T "S" .Status)}}</td></tr>
{{else}}<tr><td colspan="7" class="muted">{{index $.T "none.reports"}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{define "rreason"}}{{if eq .R "scam"}}<span class="pill bad">{{index .T "rreason.scam"}}</span>{{else}}{{tk .T "rreason." .R}}{{end}}{{end}}
{{define "rstatus"}}{{if eq .S "open"}}<span class="pill warn">{{index .T "rstatus.open"}}</span>{{else if eq .S "handled"}}<span class="pill ok">{{index .T "rstatus.handled"}}</span>{{else}}<span class="pill">{{tk .T "rstatus." .S}}</span>{{end}}{{end}}
{{define "pager"}}{{if gt .P.Pages 1}}<div class="pager">{{if .P.Prev}}<a href="{{.P.Prev}}">&larr; {{index .T "pager.prev"}}</a>{{end}}<span>{{tf .T "pager.page" .P.Page .P.Pages}}</span>{{if .P.Next}}<a href="{{.P.Next}}">{{index .T "pager.next"}} &rarr;</a>{{end}}</div>{{end}}{{end}}
`

var appBodies = map[string]string{
	"login": `{{define "body"}}<h1>{{index .T "login.title"}}</h1>
<form class="card" method="post" action="/login" novalidate>
{{template "formerr" .}}{{template "csrf" .}}
<input type="hidden" name="next" value="{{.Next}}">
<div class="hp" aria-hidden="true"><label for="website">{{index .T "reg.hp"}}</label><input type="text" id="website" name="website" tabindex="-1" autocomplete="off"></div>
<label for="email">{{index .T "reg.email"}}</label>
<input type="email" id="email" name="email" value="{{.Email}}" maxlength="64" autocomplete="username" inputmode="email" required autofocus>
<label for="password">{{index .T "reg.password"}}</label>
<input type="password" id="password" name="password" maxlength="128" autocomplete="current-password" required>
<button type="submit">{{index .T "login.submit"}}</button>
</form>
<p class="note"><a href="/forgot">{{index .T "login.forgot"}}</a></p>
<p class="note">{{index .T "login.noacc"}} <a href="/register">{{index .T "landing.register"}}</a></p>{{end}}`,

	"totp": `{{define "body"}}<h1>{{index .T "totp.title"}}</h1>
<p>{{index .T "totp.p1"}}</p>
<form class="card" method="post" action="{{if .Ticket}}/login{{else}}/login/verify{{end}}" novalidate>
{{template "formerr" .}}{{template "csrf" .}}
{{if .Ticket}}<input type="hidden" name="step" value="totp"><input type="hidden" name="ticket" value="{{.Ticket}}">{{end}}
<input type="hidden" name="next" value="{{.Next}}">
<label for="code">{{index .T "totp.code"}}</label>
<input type="text" class="code" id="code" name="code" inputmode="numeric" pattern="[0-9]*" maxlength="6" autocomplete="one-time-code" required autofocus>
{{with .Errs.code}}<p class="err">{{.}}</p>{{end}}
<button type="submit">{{index .T "totp.submit"}}</button>
</form>
<p class="note">{{index .T "totp.lost"}}</p>{{end}}`,

	"forgot": `{{define "body"}}<h1>{{index .T "forgot.title"}}</h1>
{{if .Mail}}<p>{{index .T "forgot.p1"}}</p>
<form class="card" method="post" action="/forgot" novalidate>
{{template "formerr" .}}{{template "csrf" .}}
<div class="hp" aria-hidden="true"><label for="website">{{index .T "reg.hp"}}</label><input type="text" id="website" name="website" tabindex="-1" autocomplete="off"></div>
<label for="email">{{index .T "reg.email"}}</label>
<input type="email" id="email" name="email" maxlength="64" autocomplete="email" inputmode="email" required autofocus>
<button type="submit">{{index .T "forgot.submit"}}</button>
</form>{{else}}<div class="card"><p>{{index .T "forgot.nomail"}}</p></div>{{end}}
<p class="note"><a href="/login">{{index .T "login.back"}}</a></p>{{end}}`,

	"reset": `{{define "body"}}<h1>{{index .T "reset.title"}}</h1>
<p>{{index .T "reset.p1"}}</p>
<form class="card" method="post" action="/reset" novalidate>
{{template "formerr" .}}{{template "csrf" .}}
<input type="hidden" name="token" value="{{.Token}}">
<label for="password">{{index .T "pw.new"}}</label>
<input type="password" id="password" name="password" minlength="10" maxlength="128" autocomplete="new-password" required autofocus>
{{with .Errs.password}}<p class="err">{{.}}</p>{{else}}<p class="hint">{{index $.T "reg.password.hint"}}</p>{{end}}
<label for="confirm">{{index .T "reg.confirm"}}</label>
<input type="password" id="confirm" name="confirm" minlength="10" maxlength="128" autocomplete="new-password" required>
{{with .Errs.confirm}}<p class="err">{{.}}</p>{{end}}
<button type="submit">{{index .T "reset.submit"}}</button>
</form>{{end}}`,

	"confirmemail": `{{define "body"}}<h1>{{index .T "cemail.title"}}</h1>
<form class="card" method="post" action="/confirm-email">
<p>{{tf .T "cemail.p1" .Email}}</p>
{{template "csrf" .}}<input type="hidden" name="token" value="{{.Token}}">
<button type="submit">{{index .T "cemail.submit"}}</button>
</form>{{end}}`,

	// ---- operator area ----

	"acct_overview": `{{define "body"}}{{$T := .T}}{{with .Data}}
<h1>{{tf $T "acct.hello" $.User.Name}}</h1>
{{if .AdminNoTOTP}}{{if not .TOTPOptional}}<div class="warnbox">{{index $T "tfa.need"}} <a href="/account/2fa">{{index $T "tfa.enable"}}</a></div>{{end}}{{end}}
{{if .Unverified}}<p class="muted small">{{index $T "acct.unverified"}}</p>{{end}}
<div class="grid">
<div class="tile"><span>{{index $T "st.s7"}}</span><b>{{.S7.Sessions}}</b><small>{{tf $T "st.time" (secs .S7.Seconds)}}</small></div>
<div class="tile"><span>{{index $T "st.s30"}}</span><b>{{.S30.Sessions}}</b><small>{{tf $T "st.time" (secs .S30.Seconds)}}</small></div>
<div class="tile"><span>{{index $T "st.share"}}</span><b>{{.DirectPct}}%</b><small>{{tf $T "st.share.small" .DirectPct .RelayPct}}</small></div>
<div class="tile"><span>{{index $T "st.live"}}</span><b>{{len .Live}}</b><small>{{if .Quota.MaxConcurrent}}{{tf $T "st.concurrent" .Quota.MaxConcurrent}}{{else}}{{index $T "st.concurrent.none"}}{{end}}</small></div>
</div>
<div class="cols">
<div class="card"><h3>{{index $T "st.relaymonth"}}</h3><b>{{bytes .RelayMonth}}</b> <span class="muted">/ {{if .Quota.RelayBytes}}{{bytes .Quota.RelayBytes}}{{else}}{{index $T "quota.unlimited"}}{{end}}</span>{{.RelayMeter}}<p class="hint">{{index $T "st.relaymonth.hint"}}</p></div>
<div class="card"><h3>{{index $T "st.today"}}</h3><b>{{.Today}}</b> <span class="muted">/ {{if .Quota.SessionsPerDay}}{{.Quota.SessionsPerDay}}{{else}}{{index $T "quota.unlimited"}}{{end}}</span>{{.DayMeter}}<p class="hint">{{index $T "st.today.hint"}}</p></div>
</div>
{{if .Live}}<h2>{{index $T "acct.live"}}</h2>{{template "livetable" (args "T" $T "Rows" .Live "Now" .Now "Who" false "Act" false)}}{{end}}
<h2>{{index $T "chart.sessions"}}</h2>
<div class="chart">{{.Chart}}{{template "legend" $}}</div>
<h2>{{index $T "acct.recent"}}</h2>
{{template "sesstable" (args "T" $T "Rows" .Recent "Who" false)}}
<p class="note">{{tf $T "acct.retention" .HistoryDays}}</p>
{{end}}{{end}}`,

	"acct_settings": `{{define "body"}}<h1>{{index .T "settings.title"}}</h1>
{{template "formerr" .}}
<form class="card" method="post" action="/account/settings" novalidate>
<h3>{{index .T "settings.name"}}</h3>{{template "csrf" .}}<input type="hidden" name="op" value="name">
<label for="name">{{index .T "reg.name"}}</label>
<input type="text" id="name" name="name" value="{{.Name}}" maxlength="40" autocomplete="name" required>
{{with .Errs.name}}<p class="err">{{.}}</p>{{else}}<p class="hint">{{index $.T "reg.name.hint"}}</p>{{end}}
<button type="submit" class="sm">{{index .T "save"}}</button>
</form>
<form class="card" method="post" action="/account/settings" novalidate>
<h3>{{index .T "settings.email"}}</h3>{{template "csrf" .}}<input type="hidden" name="op" value="email">
<p class="small muted">{{if .Mail}}{{index .T "settings.email.mail"}}{{else}}{{index .T "settings.email.nomail"}}{{end}}</p>
<label for="email">{{index .T "reg.email"}}</label>
<input type="email" id="email" name="email" value="{{.Email}}" maxlength="64" autocomplete="email" required>
{{with .Errs.email}}<p class="err">{{.}}</p>{{end}}
<label for="epw">{{index .T "pw.current"}}</label>
<input type="password" id="epw" name="password" maxlength="128" autocomplete="current-password" required>
{{with .Errs.email_pw}}<p class="err">{{.}}</p>{{end}}
<button type="submit" class="sm">{{index .T "settings.email.submit"}}</button>
</form>
<form class="card" method="post" action="/account/settings" novalidate>
<h3>{{index .T "settings.pw"}}</h3>{{template "csrf" .}}<input type="hidden" name="op" value="password">
<input type="text" name="username" value="{{.User.Email}}" autocomplete="username" class="hp" tabindex="-1" aria-hidden="true">
<label for="current">{{index .T "pw.current"}}</label>
<input type="password" id="current" name="current" maxlength="128" autocomplete="current-password" required>
{{with .Errs.current}}<p class="err">{{.}}</p>{{end}}
<label for="password">{{index .T "pw.new"}}</label>
<input type="password" id="password" name="password" minlength="10" maxlength="128" autocomplete="new-password" required>
{{with .Errs.password}}<p class="err">{{.}}</p>{{else}}<p class="hint">{{index $.T "reg.password.hint"}}</p>{{end}}
<label for="confirm">{{index .T "reg.confirm"}}</label>
<input type="password" id="confirm" name="confirm" minlength="10" maxlength="128" autocomplete="new-password" required>
{{with .Errs.confirm}}<p class="err">{{.}}</p>{{end}}
<label class="check"><input type="checkbox" name="revoke_tokens"><span>{{index .T "settings.pw.revoke"}}</span></label>
<p class="hint">{{index .T "settings.pw.hint"}}</p>
<button type="submit" class="sm">{{index .T "settings.pw.submit"}}</button>
</form>{{end}}`,

	"acct_security": `{{define "body"}}{{$T := .T}}{{$D := .}}{{with .Data}}<h1>{{index $T "security.title"}}</h1>
{{template "formerr" $D}}
{{if .Need2FA}}<div class="warnbox">{{index $T "tfa.need"}}</div>{{end}}
<div class="card"><h3>{{index $T "tfa.title"}}</h3>
{{if .TOTP}}<p><span class="pill ok">{{index $T "tfa.on"}}</span></p>
{{if .TOTPRequired}}<p class="small muted">{{index $T "tfa.required"}}</p>{{else}}
<form method="post" action="/account/security" novalidate>{{template "csrf" $D}}<input type="hidden" name="op" value="tfa_disable">
<div class="row3"><div><label for="tpw">{{index $T "pw.current"}}</label><input type="password" id="tpw" name="password" maxlength="128" autocomplete="current-password" required></div>
<div><label for="tcode">{{index $T "totp.code"}}</label><input type="text" id="tcode" name="code" inputmode="numeric" maxlength="6" autocomplete="one-time-code" required></div></div>
{{with $D.Errs.tfa}}<p class="err">{{.}}</p>{{end}}
<button type="submit" class="sm alt">{{index $T "tfa.disable"}}</button></form>{{end}}
{{else}}<p class="small muted">{{index $T "tfa.off.p1"}}</p>{{with $D.Errs.tfa}}<p class="err">{{.}}</p>{{end}}<a class="btn sm" href="/account/2fa">{{index $T "tfa.enable"}}</a>{{end}}
</div>
<h2>{{index $T "sec.tokens"}}</h2>
<p class="small muted">{{index $T "sec.tokens.p1"}}</p>
<div class="tbl"><table><thead><tr><th>{{index $T "col.client"}}</th><th>{{index $T "col.created"}}</th><th>{{index $T "col.lastused"}}</th><th>{{index $T "col.ip"}}</th><th>{{index $T "col.expires"}}</th><th></th></tr></thead><tbody>
{{range .Tokens}}<tr><td>{{or .Client "-"}}</td><td>{{dt .Created}}</td><td>{{dt .LastUsed}}</td><td>{{or .LastIP "-"}}</td><td>{{day .Expires}}</td><td><form method="post" action="/account/security">{{template "csrf" $D}}<input type="hidden" name="op" value="revoke_token"><input type="hidden" name="id" value="{{.ID}}"><button type="submit" class="sm alt">{{index $T "sec.revoke"}}</button></form></td></tr>
{{else}}<tr><td colspan="6" class="muted">{{index $T "none.tokens"}}</td></tr>{{end}}</tbody></table></div>
<h2>{{index $T "sec.web"}}</h2>
<div class="tbl"><table><thead><tr><th>{{index $T "col.created"}}</th><th>{{index $T "col.lastseen"}}</th><th>{{index $T "col.ip"}}</th><th>{{index $T "col.2fa"}}</th><th></th></tr></thead><tbody>
{{range .Sessions}}<tr><td>{{dt .Created}}</td><td>{{dt .LastSeen}}</td><td>{{or .IP "-"}}</td><td>{{if .MFA}}<span class="pill ok">{{index $T "yes"}}</span>{{else}}<span class="pill">{{index $T "no"}}</span>{{end}}</td><td>{{if .Current}}<span class="pill blue">{{index $T "sec.this"}}</span>{{end}}</td></tr>{{end}}</tbody></table></div>
<div class="card"><h3>{{index $T "sec.everywhere"}}</h3><p class="small muted">{{index $T "sec.everywhere.p1"}}</p>
<form method="post" action="/account/security">{{template "csrf" $D}}<input type="hidden" name="op" value="logout_all"><button type="submit" class="sm alt">{{index $T "sec.everywhere.submit"}}</button></form></div>
<form class="card" method="post" action="/account/security" novalidate><h3>{{index $T "del.title"}}</h3>
<p class="small muted">{{index $T "del.p1"}}</p>{{template "csrf" $D}}<input type="hidden" name="op" value="delete">
<div class="row3"><div><label for="dpw">{{index $T "pw.current"}}</label><input type="password" id="dpw" name="password" maxlength="128" autocomplete="current-password" required></div>
<div><label for="dconfirm">{{tf $T "del.type" $.User.Email}}</label><input type="text" id="dconfirm" name="confirm" maxlength="64" autocomplete="off" required></div></div>
{{with $D.Errs.delete}}<p class="err">{{.}}</p>{{end}}
<button type="submit" class="sm dark">{{index $T "del.submit"}}</button></form>
{{end}}{{end}}`,

	"acct_2fa": `{{define "body"}}{{$T := .T}}<h1>{{index $T "tfa.setup.title"}}</h1>
<p>{{index $T "tfa.setup.p1"}}</p>
<form class="card" method="post" action="/account/2fa" novalidate>
{{template "formerr" .}}{{template "csrf" .}}
<div class="cols"><div>{{.Data.QR}}</div><div><p class="small muted">{{index $T "tfa.setup.manual"}}</p><p><code class="secret">{{.Data.Secret}}</code></p></div></div>
<input type="hidden" name="pending" value="{{.Data.Pending}}">
<label for="code">{{index $T "tfa.setup.code"}}</label>
<input type="text" class="code" id="code" name="code" inputmode="numeric" pattern="[0-9]*" maxlength="6" autocomplete="one-time-code" required autofocus>
{{with .Errs.code}}<p class="err">{{.}}</p>{{end}}
<button type="submit">{{index $T "tfa.setup.submit"}}</button>
</form>
<p class="note"><a href="/account/security">{{index $T "cancel"}}</a></p>{{end}}`,

	// ---- admin ----

	"admin_dash": `{{define "body"}}{{$T := .T}}{{with .Data}}
<div class="top"><h1>{{index $T "adm.dash.title"}}</h1><span class="small muted">{{if .Refresh}}{{index $T "refresh.on"}} <a href="/admin?refresh=0">{{index $T "refresh.pause"}}</a>{{else}}<a href="/admin">{{index $T "refresh.resume"}}</a>{{end}}</span></div>
<div class="grid">
<div class="tile"><span>{{index $T "st.accounts"}}</span><b>{{.C.Total}}</b><small>{{tf $T "st.accounts.small" .C.Verified .C.Disabled .C.Admins}}</small></div>
<div class="tile"><span>{{index $T "st.newweek"}}</span><b>{{.C.NewWeek}}</b><small>{{if $.Mail}}{{tf $T "st.pending" .C.Pending}}{{else}}&nbsp;{{end}}</small></div>
<div class="tile"><span>{{index $T "st.hosts"}}</span><b>{{.V.HostsOnline}}</b><small>{{index $T "st.hosts.small"}}</small></div>
<div class="tile"><span>{{index $T "st.active"}}</span><b>{{.V.Accepted}}</b><small>{{tf $T "st.active.small" .V.Direct .V.Relay (len .V.Sessions)}}</small></div>
<div class="tile"><span>{{index $T "st.today.all"}}</span><b>{{.Today.Sessions}}</b><small>{{tf $T "st.failed" .Today.Failed}}</small></div>
<div class="tile"><span>{{index $T "st.s7"}}</span><b>{{.Week.Sessions}}</b><small>{{tf $T "st.time" (secs .Week.Seconds)}}</small></div>
<div class="tile"><span>{{index $T "st.s30"}}</span><b>{{.Month.Sessions}}</b><small>{{tf $T "st.share.small" (pct .Month.Direct .Month.Sessions) (pct .Month.Relay .Month.Sessions)}}</small></div>
<div class="tile"><span>{{index $T "st.relaycal"}}</span><b>{{bytes .RelayMonth}}</b><small>{{index $T "st.relaycal.small"}}</small></div>
<div class="tile"><span>{{index $T "st.logins"}}</span><b>{{.LoginsOK}}</b><small>{{tf $T "st.logins.small" .LoginsFail}}</small></div>
<div class="tile{{if .V.PakeFails24}} hot{{end}}"><span>{{index $T "st.badcodes"}}</span><b>{{.V.PakeFails24}}</b><small>{{tf $T "st.since" (dt .V.SecSince)}}</small></div>
<div class="tile{{if .V.IDLocks24}} hot{{end}}"><span>{{index $T "st.locks"}}</span><b>{{.V.IDLocks24}}</b><small>{{tf $T "st.locks.small" .V.LockedIDs}}</small></div>
<div class="tile{{if .V.Bans24}} hot{{end}}"><span>{{index $T "st.bans"}}</span><b>{{.V.Bans24}}</b><small>{{tf $T "st.bans.small" .V.BannedIPs}}</small></div>
<div class="tile{{if .ReportsOpen}} hot{{end}}"><span><a href="/admin/reports">{{index $T "st.reports"}}</a></span><b>{{.ReportsOpen}}</b><small>{{tf $T "st.reports.small" .ReportsWeek}}</small></div>
</div>
<div class="cols">
<div><h2>{{index $T "chart.sessions"}}</h2><div class="chart">{{.SessChart}}{{template "legend" $}}</div></div>
<div><h2>{{index $T "chart.relay"}}</h2><div class="chart">{{.RelayChart}}</div></div>
<div><h2>{{index $T "chart.signups"}}</h2><div class="chart">{{.SignupChart}}</div></div>
</div>
<h2>{{index $T "adm.recent"}}</h2>
{{template "sesstable" (args "T" $T "Rows" .Recent "Who" true "Adm" true)}}
<p class="note"><a href="/admin/history">{{index $T "adm.history.all"}}</a> &middot; {{tf $T "acct.retention" .HistoryDays}}</p>
{{end}}{{end}}`,

	"admin_accounts": `{{define "body"}}{{$T := .T}}{{with .Data}}{{$now := .Now}}<h1>{{index $T "adm.accounts.title"}}</h1>
<form class="search" method="get" action="/admin/accounts" role="search"><input type="search" name="q" value="{{.Q}}" maxlength="64" placeholder="{{index $T "adm.search"}}" aria-label="{{index $T "adm.search"}}"><button type="submit" class="sm">{{index $T "adm.search.submit"}}</button></form>
<p class="small muted">{{tf $T "adm.accounts.count" .P.Total}}</p>
<div class="tbl"><table><thead><tr>{{range .Cols}}<th><a href="{{.URL}}"{{if .On}} class="on"{{end}}>{{.Label}}{{if .On}}{{if .Desc}} &darr;{{else}} &uarr;{{end}}{{end}}</a></th>{{end}}<th>{{index $T "col.online"}}</th><th>{{index $T "col.insession"}}</th></tr></thead><tbody>
{{range .Rows}}<tr>
<td><a href="/admin/accounts/{{.ID}}">{{.Name}}</a>{{if .Banned}} <span class="pill bad">{{index $T "st.banned"}}</span>{{else if not .Review.IsZero}} <span class="pill bad">{{index $T "st.review"}}</span>{{else if .Disabled}} <span class="pill bad">{{index $T "st.disabled"}}</span>{{end}}{{if .OpenReports}} <span class="pill red">{{tf $T "st.reported" .OpenReports}}</span>{{end}}{{if .TOTP}} <span class="pill">2FA</span>{{end}}</td>
<td><a href="/admin/accounts/{{.ID}}">{{.Email}}</a></td>
<td>{{if .Verified}}<span class="pill ok">{{index $T "yes"}}</span>{{else}}<span class="pill warn">{{index $T "no"}}</span>{{end}}</td>
<td>{{if .IsAdmin}}<span class="pill red">{{index $T "role.admin"}}</span>{{else}}{{index $T "role.operator"}}{{end}}</td>
<td>{{day .Created}}</td>
<td>{{dt .LastLogin}}{{if .LastLoginIP}}<br><span class="muted small">{{.LastLoginIP}}</span>{{end}}</td>
<td>{{.Sessions30}}</td>
<td>{{bytes .RelayMonth}}<br><span class="muted small">/ {{if .RelayLimit}}{{bytes .RelayLimit}}{{else}}{{index $T "quota.unlimited"}}{{end}}</span></td>
<td>{{if .Online}}<span class="pill ok">{{index $T "online"}}</span>{{else}}<span class="muted">-</span>{{end}}</td>
<td>{{range .Live}}<div><code>{{.HostTID}}</code> {{if eq .State "accepted"}}{{template "path" (args "T" $T "P" .Path)}}{{else}}<span class="pill">{{tk $T "state." .State}}</span>{{end}} <span class="muted small">{{tf $T "since" (dur (.Duration $now))}}</span></div>{{else}}<span class="muted">-</span>{{end}}</td>
</tr>{{else}}<tr><td colspan="10" class="muted">{{index $T "none"}}</td></tr>{{end}}</tbody></table></div>
{{template "pager" (args "T" $T "P" .P)}}
<p class="note">{{index $T "adm.online.hint"}}</p>
{{end}}{{end}}`,

	"admin_account": `{{define "body"}}{{$T := .T}}{{$D := .}}{{with .Data}}
<p class="small"><a href="/admin/accounts">&larr; {{index $T "adm.accounts.title"}}</a></p>
<h1>{{.A.Name}} {{if .A.IsAdmin}}<span class="pill red">{{index $T "role.admin"}}</span>{{end}}{{if .A.Banned}} <span class="pill bad">{{index $T "st.banned"}}</span>{{else if not .A.Review.IsZero}} <span class="pill bad">{{index $T "st.review"}}</span>{{else if .A.Disabled}} <span class="pill bad">{{index $T "st.disabled"}}</span>{{end}}{{if .OpenReports}} <span class="pill red">{{tf $T "st.reported" .OpenReports}}</span>{{end}}{{if .Online}} <span class="pill ok">{{index $T "online"}}</span>{{end}}</h1>
{{if not .A.Review.IsZero}}{{if not .A.Banned}}<div class="warnbox">{{tf $T "review.box" (dt .A.Review)}}</div>{{end}}{{end}}
{{template "formerr" $D}}
<div class="cols">
<div class="card"><h3>{{index $T "adm.profile"}}</h3><dl class="kv">
<dt>{{index $T "col.email"}}</dt><dd>{{.A.Email}}</dd>
<dt>{{index $T "col.verified"}}</dt><dd>{{if .A.Verified}}<span class="pill ok">{{index $T "yes"}}</span> <span class="muted small">{{dt .A.VerifiedAt}}</span>{{else}}<span class="pill warn">{{index $T "no"}}</span>{{end}}</dd>
<dt>{{index $T "col.role"}}</dt><dd>{{if .A.IsAdmin}}{{index $T "role.admin"}}{{else}}{{index $T "role.operator"}}{{end}}{{if .EnvAdmin}} <span class="muted small">(RDV_ADMIN_EMAILS)</span>{{end}}</dd>
<dt>2FA</dt><dd>{{if .A.TOTP}}<span class="pill ok">{{index $T "tfa.on"}}</span>{{else}}<span class="pill">{{index $T "tfa.off"}}</span>{{end}}</dd>
<dt>{{index $T "col.created"}}</dt><dd>{{dt .A.Created}}</dd>
<dt>{{index $T "col.lastlogin"}}</dt><dd>{{dt .A.LastLogin}} {{if .A.LastLoginIP}}<span class="muted small">{{.A.LastLoginIP}}</span>{{end}}</dd>
<dt>{{index $T "col.lastused"}}</dt><dd>{{dt .TokenUsed}}</dd>
</dl></div>
<div class="card"><h3>{{index $T "adm.usage"}}</h3><dl class="kv">
<dt>{{index $T "st.s30"}}</dt><dd>{{.S30.Sessions}} &middot; {{secs .S30.Seconds}}</dd>
<dt>{{index $T "st.today"}}</dt><dd>{{.Today}} / {{if .Q.SessionsPerDay}}{{.Q.SessionsPerDay}}{{else}}{{index $T "quota.unlimited"}}{{end}}</dd>
<dt>{{index $T "quota.max"}}</dt><dd>{{if .Q.MaxConcurrent}}{{.Q.MaxConcurrent}}{{else}}{{index $T "quota.unlimited"}}{{end}}</dd>
<dt>{{index $T "st.relaymonth"}}</dt><dd>{{bytes .RelayMonth}} / {{if .Q.RelayBytesMonth}}{{bytes .Q.RelayBytesMonth}}{{else}}{{index $T "quota.unlimited"}}{{end}}</dd>
</dl>{{.RelayMeter}}</div>
</div>
{{if .Live}}<h2>{{index $T "acct.live"}}</h2>{{template "livetable" (args "T" $T "Rows" .Live "Now" .Now "Who" false "Act" false)}}{{end}}
<h2>{{index $T "adm.actions"}}</h2>
<div class="card"><div class="actions">
{{if not .A.Verified}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="verify"><button type="submit" class="sm alt">{{index $T "op.verify"}}</button></form>{{if $D.Mail}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="resend"><button type="submit" class="sm alt">{{index $T "op.resend"}}</button></form>{{end}}{{end}}
<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="reset_pw"><button type="submit" class="sm alt">{{index $T "op.reset_pw"}}</button></form>
<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="revoke_tokens"><button type="submit" class="sm alt">{{index $T "op.revoke_tokens"}}</button></form>
{{if .Live}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="terminate"><button type="submit" class="sm dark">{{index $T "op.terminate"}}</button></form>{{end}}
{{if not .Self}}{{if .A.TOTP}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="reset_2fa"><button type="submit" class="sm alt">{{index $T "op.reset_2fa"}}</button></form>{{end}}
{{if .A.IsAdmin}}{{if not .EnvAdmin}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="demote"><button type="submit" class="sm alt">{{index $T "op.demote"}}</button></form>{{end}}{{else}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="promote"><button type="submit" class="sm alt">{{index $T "op.promote"}}</button></form>{{end}}
{{if .A.Disabled}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="enable"><button type="submit" class="sm alt">{{if .A.Banned}}{{index $T "op.unban"}}{{else}}{{index $T "op.enable"}}{{end}}</button></form>{{else}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="disable"><button type="submit" class="sm dark">{{index $T "op.disable"}}</button></form>{{end}}
{{if not .A.Banned}}<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="ban"><button type="submit" class="sm dark">{{index $T "op.ban"}}</button></form>{{end}}
<form method="post" action="/admin/accounts/{{$D.Data.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="delete"><button type="submit" class="sm dark">{{index $T "op.delete"}}</button></form>{{end}}
</div></div>
{{if .Reports}}<h2>{{index $T "adm.reports.acct"}}</h2>{{template "reporttable" (args "T" $T "Rows" .Reports "Who" false)}}{{end}}
<form class="card" method="post" action="/admin/accounts/{{$D.Data.A.ID}}" novalidate><h3>{{index $T "op.quota"}}</h3>{{template "csrf" $D}}<input type="hidden" name="op" value="quota">
<p class="small muted">{{index $T "quota.hint"}}</p>
<div class="row3">
<div><label for="qm">{{index $T "quota.max"}}</label><input type="number" id="qm" name="max_sessions" min="0" max="10000" value="{{deref .A.Quota.MaxSessions}}" placeholder="{{.Defaults.MaxConcurrent}}"></div>
<div><label for="qd">{{index $T "quota.day"}}</label><input type="number" id="qd" name="sessions_day" min="0" max="1000000" value="{{deref .A.Quota.SessionsDay}}" placeholder="{{.Defaults.SessionsPerDay}}"></div>
<div><label for="qr">{{index $T "quota.relay"}}</label><input type="number" id="qr" name="relay_gb" min="0" max="1000000" value="{{gb .A.Quota.RelayBytes}}" placeholder="{{div1e9 .Defaults.RelayBytesMonth}}"></div>
</div>
<button type="submit" class="sm">{{index $T "save"}}</button></form>
<h2>{{index $T "sec.tokens"}}</h2>
<div class="tbl"><table><thead><tr><th>{{index $T "col.client"}}</th><th>{{index $T "col.created"}}</th><th>{{index $T "col.lastused"}}</th><th>{{index $T "col.ip"}}</th><th>{{index $T "col.expires"}}</th></tr></thead><tbody>
{{range .Tokens}}<tr><td>{{or .Client "-"}}</td><td>{{dt .Created}}</td><td>{{dt .LastUsed}}</td><td>{{or .LastIP "-"}}</td><td>{{day .Expires}}</td></tr>{{else}}<tr><td colspan="5" class="muted">{{index $T "none.tokens"}}</td></tr>{{end}}</tbody></table></div>
<h2>{{index $T "sec.web"}}</h2>
<div class="tbl"><table><thead><tr><th>{{index $T "col.created"}}</th><th>{{index $T "col.lastseen"}}</th><th>{{index $T "col.ip"}}</th><th>{{index $T "col.2fa"}}</th></tr></thead><tbody>
{{range .WebSessions}}<tr><td>{{dt .Created}}</td><td>{{dt .LastSeen}}</td><td>{{or .IP "-"}}</td><td>{{if .MFA}}{{index $T "yes"}}{{else}}{{index $T "no"}}{{end}}</td></tr>{{else}}<tr><td colspan="4" class="muted">{{index $T "none"}}</td></tr>{{end}}</tbody></table></div>
<h2>{{index $T "adm.sessions"}}</h2>
{{template "sesstable" (args "T" $T "Rows" .History "Who" false "Adm" true)}}
{{template "pager" (args "T" $T "P" .P)}}
<h2>{{index $T "adm.logins"}}</h2>
<div class="tbl"><table><thead><tr><th>{{index $T "col.time"}}</th><th>{{index $T "col.kind"}}</th><th>{{index $T "col.result"}}</th><th>{{index $T "col.ip"}}</th></tr></thead><tbody>
{{range .Logins}}<tr><td>{{dt .At}}</td><td>{{tk $T "kind." .Kind}}</td><td>{{if .OK}}<span class="pill ok">{{index $T "login.ok"}}</span>{{else}}<span class="pill bad">{{tk $T "lfail." .Reason}}</span>{{end}}</td><td>{{or .IP "-"}}</td></tr>{{else}}<tr><td colspan="4" class="muted">{{index $T "none"}}</td></tr>{{end}}</tbody></table></div>
{{end}}{{end}}`,

	"admin_confirm": `{{define "body"}}{{$T := .T}}{{$D := .}}{{with .Data}}<h1>{{.OpLabel}}</h1>
<form class="card" method="post" action="{{.PostTo}}" novalidate>
{{template "formerr" $D}}
{{if .Target.Email}}<p>{{tf $T "confirm.target" .Target.Name .Target.Email}}</p>{{end}}
{{range .Lines}}<p class="small">{{.}}</p>{{end}}
<p class="small muted">{{index $T "confirm.p1"}}</p>
{{template "csrf" $D}}{{range .Hidden}}<input type="hidden" name="{{index . 0}}" value="{{index . 1}}">{{end}}<input type="hidden" name="confirmed" value="1">
{{if .Typed}}<label for="typed">{{tf $T "del.type" .Target.Email}}</label><input type="text" id="typed" name="typed" maxlength="64" autocomplete="off" required>{{end}}
<button type="submit"{{if .Danger}} class="dark"{{end}}>{{index $T "confirm.submit"}}</button>
<a class="btn alt" href="{{.Back}}">{{index $T "cancel"}}</a>
</form>{{end}}{{end}}`,

	"admin_link": `{{define "body"}}{{$T := .T}}{{with .Data}}<h1>{{index $T "link.title"}}</h1>
<div class="card"><p>{{tf $T "link.p1" .Target.Email}}</p><p><code>{{.Link}}</code></p><p class="small muted">{{index $T "link.p2"}}</p>
<a class="btn alt" href="{{.Back}}">{{index $T "back"}}</a></div>{{end}}{{end}}`,

	"admin_live": `{{define "body"}}{{$T := .T}}{{with .Data}}
<div class="top"><h1>{{index $T "adm.live.title"}}</h1><span class="small muted">{{if .Refresh}}{{index $T "refresh.on"}} <a href="/admin/live?refresh=0">{{index $T "refresh.pause"}}</a>{{else}}<a href="/admin/live">{{index $T "refresh.resume"}}</a>{{end}}</span></div>
{{template "formerr" $}}
<div class="grid">
<div class="tile"><span>{{index $T "st.hosts"}}</span><b>{{.V.HostsOnline}}</b><small>{{index $T "st.hosts.small"}}</small></div>
<div class="tile"><span>{{index $T "st.active"}}</span><b>{{.V.Accepted}}</b><small>{{tf $T "st.active.small" .V.Direct .V.Relay (len .V.Sessions)}}</small></div>
<div class="tile"><span>{{index $T "st.locks.now"}}</span><b>{{.V.LockedIDs}}</b><small>{{tf $T "st.bans.small" .V.BannedIPs}}</small></div>
</div>
<h2>{{index $T "adm.live.sessions"}}</h2>
{{template "livetable" (args "T" $T "Rows" .Rows "Now" .V.At "Who" true "Act" true "D" $)}}
<p class="note">{{index $T "adm.live.hint"}}</p>
{{end}}{{end}}`,

	"admin_history": `{{define "body"}}{{$T := .T}}{{with .Data}}<h1>{{index $T "adm.history.title"}}</h1>
<p class="small muted">{{tf $T "acct.retention" .Days}}</p>
{{template "sesstable" (args "T" $T "Rows" .Rows "Who" true "Adm" true)}}
{{template "pager" (args "T" $T "P" .P)}}{{end}}{{end}}`,

	"admin_audit": `{{define "body"}}{{$T := .T}}{{with .Data}}<h1>{{index $T "adm.audit.title"}}</h1>
<p class="small muted">{{tf $T "adm.audit.p1" .Days}}</p>
<div class="tbl"><table><thead><tr><th>{{index $T "col.time"}}</th><th>{{index $T "col.admin"}}</th><th>{{index $T "col.action"}}</th><th>{{index $T "col.target"}}</th><th>{{index $T "col.detail"}}</th><th>{{index $T "col.ip"}}</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{dt .At}}</td><td>{{.Actor}}</td><td>{{tk $T "audit." .Action}}</td><td>{{if .TargetID}}<a href="/admin/accounts/{{.TargetID}}">{{.Target}}</a>{{else}}{{.Target}}{{end}}</td><td class="wrap">{{.Detail}}</td><td>{{or .IP "-"}}</td></tr>
{{else}}<tr><td colspan="6" class="muted">{{index $T "none"}}</td></tr>{{end}}</tbody></table></div>
{{template "pager" (args "T" $T "P" .P)}}{{end}}{{end}}`,

	"admin_reports": `{{define "body"}}{{$T := .T}}{{with .Data}}<h1>{{index $T "adm.reports.title"}}</h1>
<div class="tabs">{{range .Tabs}}<a href="{{.URL}}"{{if .On}} class="on"{{end}}>{{.Label}}</a>{{end}}</div>
<p class="small muted">{{tf $T "adm.reports.count" .P.Total}}</p>
{{template "reporttable" (args "T" $T "Rows" .Rows "Who" true)}}
{{template "pager" (args "T" $T "P" .P)}}
<p class="note">{{tf $T "adm.reports.retention" .Days}}</p>{{end}}{{end}}`,

	"admin_report": `{{define "body"}}{{$T := .T}}{{$D := .}}{{with .Data}}
<p class="small"><a href="/admin/reports">&larr; {{index $T "adm.reports.title"}}</a></p>
<h1>{{tf $T "adm.report.h" .R.ID}} {{template "rstatus" (args "T" $T "S" .R.Status)}}</h1>
{{template "formerr" $D}}
<div class="cols">
<div class="card"><h3>{{index $T "adm.report.what"}}</h3><dl class="kv">
<dt>{{index $T "col.time"}}</dt><dd>{{dt .R.Created}}</dd>
<dt>{{index $T "col.rreason"}}</dt><dd>{{template "rreason" (args "T" $T "R" .R.Reason)}}</dd>
<dt>{{index $T "col.reporterip"}}</dt><dd>{{or .R.ReporterIP "-"}}</dd>
<dt>{{index $T "col.status"}}</dt><dd>{{template "rstatus" (args "T" $T "S" .R.Status)}}{{if .R.ClosedBy}} <span class="muted small">{{tf $T "rclosed" .R.ClosedBy (dt .R.Closed)}}</span>{{end}}</dd>
</dl>
<h3>{{index $T "col.rtext"}}</h3>{{if .R.Text}}<p class="pre">{{.R.Text}}</p>{{else}}<p class="muted small">{{index $T "rtext.none"}}</p>{{end}}
</div>
<div class="card"><h3>{{index $T "adm.report.session"}}</h3><dl class="kv">
<dt>{{index $T "col.operator"}}</dt><dd>{{if .HasAccount}}<a href="/admin/accounts/{{.A.ID}}">{{.A.Name}} &lt;{{.A.Email}}&gt;</a>{{if .A.Banned}} <span class="pill bad">{{index $T "st.banned"}}</span>{{else if not .A.Review.IsZero}} <span class="pill bad">{{index $T "st.review"}}</span>{{else if .A.Disabled}} <span class="pill bad">{{index $T "st.disabled"}}</span>{{end}}{{else if .R.AccountID}}acct:{{.R.AccountID}}{{else}}<span class="pill">{{index $T "key"}}</span> {{.R.KeyLabel}}{{end}}</dd>
<dt>{{index $T "adm.report.shown"}}</dt><dd>{{or .R.Operator "-"}}</dd>
<dt>{{index $T "col.host"}}</dt><dd><code>{{.R.HostTID}}</code></dd>
<dt>{{index $T "col.sid"}}</dt><dd><code>{{.R.SID}}</code></dd>
{{if .Hist}}<dt>{{index $T "col.start"}}</dt><dd>{{dt .Hist.Started}}</dd>
<dt>{{index $T "col.accepted"}}</dt><dd>{{dt .Hist.Accepted}}</dd>
<dt>{{index $T "col.end"}}</dt><dd>{{dt .Hist.Ended}}</dd>
<dt>{{index $T "col.duration"}}</dt><dd>{{dur .Hist.Duration}}</dd>
<dt>{{index $T "col.path"}}</dt><dd>{{template "path" (args "T" $T "P" .Hist.Path)}}</dd>
<dt>{{index $T "col.relayed"}}</dt><dd>{{bytes .Hist.Relayed}}</dd>
<dt>{{index $T "col.reason"}}</dt><dd>{{tk $T "reason." .Hist.EndReason}}</dd>
{{else}}<dt>{{index $T "col.start"}}</dt><dd>{{dt .R.Started}}</dd>
<dt>{{index $T "col.accepted"}}</dt><dd>{{dt .R.Accepted}}</dd>
<dt>{{index $T "col.end"}}</dt><dd>{{if .Live}}<span class="pill red">{{index $T "rsession.live"}}</span>{{else}}{{dt .R.Ended}}{{end}}</dd>{{end}}
</dl></div>
</div>
<h2>{{index $T "adm.actions"}}</h2>
<div class="card"><div class="actions">
{{if eq .R.Status "open"}}<form method="post" action="{{.Ret}}">{{template "csrf" $D}}<input type="hidden" name="op" value="handled"><button type="submit" class="sm">{{index $T "op.report_handled"}}</button></form>
<form method="post" action="{{.Ret}}">{{template "csrf" $D}}<input type="hidden" name="op" value="not_abuse"><button type="submit" class="sm alt">{{index $T "op.report_not_abuse"}}</button></form>
{{else}}<form method="post" action="{{.Ret}}">{{template "csrf" $D}}<input type="hidden" name="op" value="reopen"><button type="submit" class="sm alt">{{index $T "op.report_reopen"}}</button></form>{{end}}
{{if .HasAccount}}{{if not .Self}}{{if not .A.Disabled}}<form method="post" action="/admin/accounts/{{.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="disable"><input type="hidden" name="ret" value="{{.Ret}}"><button type="submit" class="sm dark">{{index $T "op.disable_operator"}}</button></form>{{end}}
{{if not .A.Banned}}<form method="post" action="/admin/accounts/{{.A.ID}}">{{template "csrf" $D}}<input type="hidden" name="op" value="ban"><input type="hidden" name="ret" value="{{.Ret}}"><button type="submit" class="sm dark">{{index $T "op.ban"}}</button></form>{{end}}{{end}}{{end}}
</div>
<p class="hint">{{index $T "adm.report.actions.hint"}}</p></div>
<form class="card" method="post" action="{{.Ret}}" novalidate><h3>{{index $T "adm.report.notes"}}</h3>
{{range .R.Notes}}<div class="note-item"><p class="small muted">{{dt .At}} &middot; {{.Author}}</p><p class="pre">{{.Text}}</p></div>{{else}}<p class="small muted">{{index $T "none.notes"}}</p>{{end}}
{{template "csrf" $D}}<input type="hidden" name="op" value="note">
<label for="note">{{index $T "adm.report.note"}}</label><textarea id="note" name="note" maxlength="2000" required></textarea>
<button type="submit" class="sm">{{index $T "adm.report.note.submit"}}</button></form>
{{if .Others}}<h2>{{index $T "adm.report.others"}}</h2>{{template "reporttable" (args "T" $T "Rows" .Others "Who" false)}}{{end}}
{{if .Sessions}}<h2>{{index $T "adm.report.sessions"}}</h2>{{template "sesstable" (args "T" $T "Rows" .Sessions "Who" false "Adm" true)}}{{end}}
<p class="note">{{tf $T "adm.reports.retention" .Retention}}</p>
{{end}}{{end}}`,
}
