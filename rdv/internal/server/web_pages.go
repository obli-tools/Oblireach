package server

import "html/template"

// pageCSS is the only style of the pages; the CSP allows it by hash, so it
// must be emitted byte for byte. Dark, brand red #E03A3A, buttons #C83232,
// no borders, mobile first.
const pageCSS = `:root{color-scheme:dark}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:#111114;color:#ececf1;font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif}
main{max-width:460px;margin:0 auto;padding:36px 16px 48px}
.brand{display:flex;align-items:center;gap:10px;margin:0 0 26px;font-weight:600;font-size:18px;color:#ececf1;text-decoration:none}
.brand b{color:#E03A3A;font-weight:600}
h1{font-size:24px;line-height:1.25;margin:0 0 14px}
p{margin:0 0 14px;color:#c9c9d1}
.card{background:#1b1b20;border-radius:14px;padding:22px 20px}
label{display:block;font-size:14px;color:#a6a6b0;margin:16px 0 6px}
input[type=text],input[type=email],input[type=password]{width:100%;padding:12px 14px;border:0;border-radius:10px;background:#27272e;color:#f4f4f6;font:inherit;outline:none}
input[type=text]:focus,input[type=email]:focus,input[type=password]:focus{box-shadow:0 0 0 2px #E03A3A}
.hint{font-size:13px;color:#8d8d98;margin:6px 0 0}
.check{display:flex;gap:10px;align-items:flex-start;margin:18px 0 0;font-size:14px;color:#c9c9d1}
.check input{margin:3px 0 0;accent-color:#C83232;width:18px;height:18px;flex:none}
button,.btn{display:block;width:100%;margin:22px 0 0;padding:13px 16px;border:0;border-radius:10px;background:#C83232;color:#fff;font:inherit;font-weight:600;text-align:center;text-decoration:none;cursor:pointer}
button:hover,.btn:hover,button:focus-visible,.btn:focus-visible{background:#a92727;outline:none}
a{color:#ff7272}
.err{color:#ff8f8f;font-size:14px;margin:6px 0 0}
.formerr{background:#3a1d20;color:#ffb3b3;border-radius:10px;padding:10px 14px;margin:0 0 6px;font-size:14px}
.note{font-size:14px;color:#9a9aa5;margin:18px 0 0}
.hp{position:absolute;left:-10000px;top:auto;width:1px;height:1px;overflow:hidden}
footer{margin:28px 0 0;font-size:13px;color:#7c7c88;display:flex;gap:16px;flex-wrap:wrap}
footer a{color:#9a9aa5}
main.wide{max-width:1180px}
.top{display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap;margin:0 0 16px}
.top .brand{margin:0}
.who{display:flex;gap:12px;align-items:center;font-size:14px;color:#9a9aa5;flex-wrap:wrap}
.who form{margin:0}
.tabs{display:flex;gap:6px;flex-wrap:wrap;margin:0 0 22px}
.tabs a{padding:8px 14px;border-radius:9px;background:#1b1b20;color:#c9c9d1;text-decoration:none;font-size:14px}
.tabs a:hover{background:#25252c}
.tabs a.on{background:#C83232;color:#fff}
.tabs .sep{width:10px}
h2{font-size:18px;margin:28px 0 12px}
h3{font-size:15px;margin:0 0 10px;color:#ececf1}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(165px,1fr));gap:12px;margin:0 0 8px}
.tile{background:#1b1b20;border-radius:14px;padding:14px 16px}
.tile b{display:block;font-size:26px;line-height:1.2;font-weight:600;color:#f4f4f6}
.tile span{display:block;font-size:13px;color:#9a9aa5}
.tile small{display:block;font-size:12px;color:#7c7c88;margin-top:4px}
.tile.hot b{color:#ff7272}
.cols{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:14px}
.chart{background:#1b1b20;border-radius:14px;padding:14px 16px 10px}
.chart svg{display:block;width:100%;height:auto}
.legend{display:flex;gap:14px;font-size:12px;color:#9a9aa5;margin:6px 0 0;flex-wrap:wrap}
.dot{display:inline-block;width:9px;height:9px;border-radius:3px;margin-right:5px;vertical-align:0}
.dot.d{background:#4f9dde}.dot.r{background:#E03A3A}.dot.o{background:#8d8d98}
.tbl{overflow-x:auto;background:#1b1b20;border-radius:14px}
table{width:100%;border-collapse:collapse;font-size:14px}
th,td{padding:9px 12px;text-align:left;white-space:nowrap;vertical-align:top}
th{color:#9a9aa5;font-weight:500;background:#202027}
th a{color:#9a9aa5;text-decoration:none}
th a.on{color:#f4f4f6}
tr:nth-child(even) td{background:#1e1e24}
td.wrap{white-space:normal;min-width:180px}
td a{color:#ececf1}
.pill{display:inline-block;padding:2px 9px;border-radius:999px;font-size:12px;background:#2a2a32;color:#c9c9d1}
.pill.ok{background:#173a26;color:#8fe0ad}
.pill.warn{background:#3d3215;color:#f1cf78}
.pill.bad{background:#3a1d20;color:#ffb3b3}
.pill.red{background:#C83232;color:#fff}
.pill.blue{background:#16304a;color:#9ccbf2}
.muted{color:#8d8d98}
.small{font-size:13px}
.flash{background:#173a26;color:#b5f0c9;border-radius:10px;padding:10px 14px;margin:0 0 16px;font-size:14px}
.warnbox{background:#3d3215;color:#f6dc97;border-radius:10px;padding:10px 14px;margin:0 0 16px;font-size:14px}
.search{display:flex;gap:8px;margin:0 0 14px}
.search input{flex:1}
.search button{width:auto;margin:0}
.pager{display:flex;gap:16px;margin:12px 0;font-size:14px;color:#9a9aa5;align-items:center}
button.alt,.btn.alt{background:#2a2a32}
button.alt:hover,.btn.alt:hover{background:#34343d}
button.dark{background:#5a1a1a}
button.dark:hover{background:#7a2020}
button.sm,.btn.sm{display:inline-block;width:auto;margin:0;padding:7px 12px;font-size:13px}
button.link{display:inline;width:auto;margin:0;padding:0;background:none;color:#ff7272;font-weight:400;text-decoration:underline}
button.link:hover{background:none;color:#ff9a9a}
.actions{display:flex;flex-wrap:wrap;gap:10px;align-items:center}
.actions form{margin:0}
.card+.card,.tbl+.card,.card+.tbl{margin-top:14px}
.cols>.card,.cols>div>.card{margin-top:0}
.card h2:first-child,.card h3:first-child{margin-top:0}
.kv{display:grid;grid-template-columns:max-content 1fr;gap:6px 18px;font-size:14px;margin:0}
.kv dt{color:#9a9aa5}
.kv dd{margin:0;min-width:0;overflow-wrap:anywhere}
.qr{display:block;width:220px;max-width:100%;border-radius:10px}
code{background:#27272e;padding:2px 6px;border-radius:6px;font-size:14px;overflow-wrap:anywhere}
.secret{font-size:17px;letter-spacing:1px}
.meter{display:block;width:100%;height:10px;margin:8px 0 4px}
.row3{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px}
input[type=number]{width:100%;padding:12px 14px;border:0;border-radius:10px;background:#27272e;color:#f4f4f6;font:inherit;outline:none}
input[type=number]:focus,input[type=search]:focus{box-shadow:0 0 0 2px #E03A3A}
input[type=search]{width:100%;padding:11px 14px;border:0;border-radius:10px;background:#27272e;color:#f4f4f6;font:inherit;outline:none}
.code{font-size:22px;letter-spacing:6px;text-align:center}
textarea{display:block;width:100%;min-height:96px;padding:12px 14px;border:0;border-radius:10px;background:#27272e;color:#f4f4f6;font:inherit;outline:none;resize:vertical}
textarea:focus{box-shadow:0 0 0 2px #E03A3A}
.pre{white-space:pre-wrap;overflow-wrap:anywhere;margin:0}
.note-item{background:#202027;border-radius:10px;padding:10px 14px;margin:0 0 8px}
@media (max-width:640px){th,td{padding:8px 9px}.tile b{font-size:22px}}
`

const brandHTML = `<a class="brand" href="/"><svg width="28" height="28" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="8" fill="#E03A3A"/><circle cx="16" cy="16" r="8" fill="none" stroke="#fff" stroke-width="3.2"/><circle cx="16" cy="16" r="2.6" fill="#fff"/></svg><span>Obli<b>reach</b></span></a>`

const layoutTmpl = `{{define "layout"}}<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="same-origin">
<meta name="robots" content="noindex">
{{if .Refresh}}<meta http-equiv="refresh" content="{{.Refresh}}">{{end}}
<title>{{.Title}} - {{.ServerName}}</title>
<style>` + pageCSS + `</style>
</head>
<body>
<main{{if .Wide}} class="wide"{{end}}>
{{if .User}}<header class="top">` + brandHTML + `<div class="who"><span>{{.User.Email}}{{if .User.IsAdmin}} <span class="pill red">{{index .T "role.admin"}}</span>{{end}}</span><form method="post" action="/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="ts" value="{{.TS}}"><button type="submit" class="link">{{index .T "nav.logout"}}</button></form></div></header>
<nav class="tabs">{{if .IsAdmin}}<a href="/admin"{{if eq .Nav "dash"}} class="on"{{end}}>{{index .T "nav.dash"}}</a><a href="/admin/accounts"{{if eq .Nav "accounts"}} class="on"{{end}}>{{index .T "nav.accounts"}}</a><a href="/admin/live"{{if eq .Nav "live"}} class="on"{{end}}>{{index .T "nav.live"}}</a><a href="/admin/reports"{{if eq .Nav "reports"}} class="on"{{end}}>{{index .T "nav.reports"}}</a><a href="/admin/history"{{if eq .Nav "history"}} class="on"{{end}}>{{index .T "nav.history"}}</a><a href="/admin/audit"{{if eq .Nav "audit"}} class="on"{{end}}>{{index .T "nav.audit"}}</a><span class="sep"></span>{{end}}<a href="/account"{{if eq .Nav "acct"}} class="on"{{end}}>{{index .T "nav.acct"}}</a><a href="/account/settings"{{if eq .Nav "settings"}} class="on"{{end}}>{{index .T "nav.settings"}}</a><a href="/account/security"{{if eq .Nav "security"}} class="on"{{end}}>{{index .T "nav.security"}}</a></nav>
{{else}}` + brandHTML + `{{end}}
{{if .Flash}}<div class="flash" role="status">{{.Flash}}</div>{{end}}
{{template "body" .}}
<footer><a href="/">{{index .T "nav.home"}}</a><a href="/login-help">{{index .T "nav.help"}}</a><a href="/terms">{{index .T "nav.terms"}}</a><span>{{.ServerName}} {{.Version}}</span></footer>
</main>
</body>
</html>{{end}}`

var pageBodies = map[string]string{
	"message": `{{define "body"}}<h1>{{.Title}}</h1>{{range .Paras}}<p>{{.}}</p>{{end}}{{if .Link}}<a class="btn" href="{{.Link}}">{{.LinkText}}</a>{{end}}{{end}}`,

	"landing": `{{define "body"}}<h1>{{index .T "landing.title"}}</h1>
<p>{{index .T "landing.p1"}}</p>
<p>{{index .T "landing.p2"}}</p>
{{if .Accounts}}<a class="btn" href="/login">{{index .T "landing.login"}}</a>
<a class="btn alt" href="/register">{{index .T "landing.register"}}</a>
<p class="note"><a href="/login-help">{{index .T "nav.help"}}</a></p>{{else}}<p class="note">{{index .T "landing.noacc"}}</p>{{end}}{{end}}`,

	"register": `{{define "body"}}<h1>{{index .T "reg.title"}}</h1>
<p>{{index .T "reg.intro"}}</p>
<form class="card" method="post" action="/register" autocomplete="on" novalidate>
{{if .FormErr}}<div class="formerr" role="alert">{{.FormErr}}</div>{{end}}
<input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="ts" value="{{.TS}}">
<div class="hp" aria-hidden="true"><label for="website">{{index .T "reg.hp"}}</label><input type="text" id="website" name="website" tabindex="-1" autocomplete="off"></div>
<label for="name">{{index .T "reg.name"}}</label>
<input type="text" id="name" name="name" value="{{.Name}}" maxlength="40" autocomplete="name" required>
{{with .Errs.name}}<p class="err">{{.}}</p>{{else}}<p class="hint">{{index $.T "reg.name.hint"}}</p>{{end}}
<label for="email">{{index .T "reg.email"}}</label>
<input type="email" id="email" name="email" value="{{.Email}}" maxlength="64" autocomplete="email" inputmode="email" required>
{{with .Errs.email}}<p class="err">{{.}}</p>{{end}}
<label for="password">{{index .T "reg.password"}}</label>
<input type="password" id="password" name="password" minlength="10" maxlength="128" autocomplete="new-password" required>
{{with .Errs.password}}<p class="err">{{.}}</p>{{else}}<p class="hint">{{index $.T "reg.password.hint"}}</p>{{end}}
<label for="confirm">{{index .T "reg.confirm"}}</label>
<input type="password" id="confirm" name="confirm" minlength="10" maxlength="128" autocomplete="new-password" required>
{{with .Errs.confirm}}<p class="err">{{.}}</p>{{end}}
<label class="check"><input type="checkbox" name="terms" required><span>{{index .T "reg.terms.pre"}} <a href="/terms" target="_blank" rel="noopener">{{index .T "reg.terms.link"}}</a></span></label>
{{with .Errs.terms}}<p class="err">{{.}}</p>{{end}}
<button type="submit">{{index .T "reg.submit"}}</button>
</form>
<p class="note">{{index .T "reg.have"}}</p>{{end}}`,

	"verify": `{{define "body"}}<h1>{{index .T "verify.title"}}</h1>
<form class="card" method="post" action="/verify">
{{if .FormErr}}<div class="formerr" role="alert">{{.FormErr}}</div>{{end}}
<p>{{index .T "verify.p1"}}</p>
<input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="ts" value="{{.TS}}">
<input type="hidden" name="token" value="{{.Token}}">
<button type="submit">{{index .T "verify.submit"}}</button>
</form>{{end}}`,
}

func parsePages() map[string]*template.Template {
	base := template.Must(template.New("layout").Funcs(pageFuncs).Parse(layoutTmpl))
	template.Must(base.Parse(partialsTmpl))
	out := map[string]*template.Template{}
	for name, body := range pageBodies {
		out[name] = template.Must(template.Must(base.Clone()).Parse(body))
	}
	return out
}
