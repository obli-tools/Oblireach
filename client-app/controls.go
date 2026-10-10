package main

// controls.go — the page's BASE look of the native controls (no native
// light borders; design rule: no borders, surfaces differ by colour
// variants only).
//
// WebView2 draws a <button>, <input>, <select> or <textarea> without a class
// rule of its own with Chromium's native look: a 2px light grey border
// (buttonborder), a white/black focus ring, the system checkbox. Every
// control of the app (main window, detached windows, dialogs, pages, panels,
// menus: one page, ui.go buildUI) starts here from a neutral one instead:
// no border, no outline, no native appearance, the page's font family; the
// focus seen at the keyboard is a soft ring tinted with the accent, never
// white; the fields are a step of colour away from their surface
// (--input-bg), never a line.
//
// Also here: the checkboxes and radio buttons drawn in the theme's colours
// (a tint of the ink, the accent fill once checked) and the common
// segmented control (.seg): a group of options side by side, one active
// ("Vault credential / Its own password or key", "SSH / RDP", the
// deployment's OS tabs).
//
// controlsCSS is injected with the theme's variables, BEFORE every other
// rule (ui.go buildUI, the THEME_CSS <style>): its element selectors are
// the floor, a component's own rule (a class) always wins over them. Not
// through Sprintf: '%' is safe; no backtick here.

const controlsCSS = `
/* ── Native controls, neutralised (controls.go) ── */
button,input,select,textarea,[role=button]{border:none;outline:none;font-family:inherit;-webkit-appearance:none;appearance:none}
input[type=color],input[type=range],input[type=file]{-webkit-appearance:auto;appearance:auto}
:focus,:focus-visible{outline:none}
/* The focus at the keyboard: a soft ring of the accent (never white). A
   component's own :focus rule (a class) wins over it. */
button:focus-visible,[role=button]:focus-visible,[role=tab]:focus-visible,[role=option]:focus-visible,a[href]:focus-visible,summary:focus-visible{box-shadow:0 0 0 3px rgb(var(--accent-rgb) / .3)}
input:focus-visible,select:focus-visible,textarea:focus-visible{box-shadow:0 0 0 3px rgb(var(--accent-rgb) / .3)}
:where([tabindex]:not([tabindex="-1"]):not(canvas):not(tr):not(.xterm-helper-textarea)):focus-visible{box-shadow:0 0 0 3px rgb(var(--accent-rgb) / .26)}
/* A field without a rule of its own: a step of colour from its surface. */
input:not([type]),input[type=text],input[type=password],input[type=number],input[type=search],input[type=email],input[type=url],input[type=tel],select,textarea{background-color:var(--input-bg);color:var(--text);border-radius:8px}
input::placeholder,textarea::placeholder{color:var(--muted2);opacity:1}
/* Checkboxes and radio buttons: a tint of the ink, the accent fill once
   checked (a check / a dot in the text colour on the accent), no border. */
input[type=checkbox],input[type=radio]{-webkit-appearance:none;appearance:none;display:inline-grid;place-content:center;flex-shrink:0;width:15px;height:15px;padding:0;border:none;border-radius:4px;background:rgb(var(--ink) / .14);cursor:pointer;vertical-align:middle;transition:background .12s}
input[type=radio]{border-radius:50%}
input[type=checkbox]:hover,input[type=radio]:hover{background:rgb(var(--ink) / .22)}
input[type=checkbox]:checked,input[type=checkbox]:indeterminate,input[type=radio]:checked{background:var(--accent-fill)}
input[type=checkbox]:checked:hover,input[type=checkbox]:indeterminate:hover,input[type=radio]:checked:hover{background:var(--accent-h)}
input[type=checkbox]::before{content:'';width:9px;height:9px;transform:scale(0);transition:transform .1s;background:var(--on-accent);clip-path:polygon(14% 44%,0 65%,50% 100%,100% 16%,80% 0,43% 62%)}
input[type=checkbox]:checked::before{transform:scale(1)}
input[type=checkbox]:indeterminate::before{transform:scale(1);clip-path:inset(40% 6% 40% 6%)}
input[type=radio]::before{content:'';width:6px;height:6px;border-radius:50%;transform:scale(0);transition:transform .1s;background:var(--on-accent)}
input[type=radio]:checked::before{transform:scale(1)}
input[type=checkbox]:disabled,input[type=radio]:disabled{opacity:.45;cursor:default}
input[type=checkbox]:focus-visible,input[type=radio]:focus-visible{box-shadow:0 0 0 3px rgb(var(--accent-rgb) / .3)}

/* ── Segmented control (controls.go): options side by side, one active ──
   The track a darker step (--input-bg), the active option the accent fill,
   the others transparent with a muted text, a tint of the ink on hover; no
   border. The active one: .on / .active / aria-checked / aria-pressed. */
.seg{display:flex;flex-wrap:wrap;gap:2px;padding:3px;border-radius:10px;background:var(--input-bg)}
.seg>*{flex:1 1 auto;display:inline-flex;align-items:center;justify-content:center;gap:6px;margin:0;padding:6px 12px;border:none;outline:none;border-radius:7px;background:transparent;color:var(--muted);font-size:12px;font-weight:500;line-height:1.3;cursor:pointer;white-space:nowrap;user-select:none;transition:background .12s,color .12s,box-shadow .12s}
.seg>*:hover{background:rgb(var(--ink) / .07);color:var(--text)}
.seg>.on,.seg>.active,.seg>[aria-checked=true],.seg>[aria-pressed=true]{background:var(--accent-fill);color:var(--on-accent);box-shadow:0 1px 3px rgb(var(--accent-rgb) / .3)}
.seg>.on:hover,.seg>.active:hover,.seg>[aria-checked=true]:hover,.seg>[aria-pressed=true]:hover{background:var(--accent-h);color:var(--on-accent)}
.seg>*:focus-visible{box-shadow:0 0 0 3px rgb(var(--accent-rgb) / .3)}
.seg>:disabled,.seg>[aria-disabled=true]{opacity:.45;cursor:default}
.seg.fit>*{flex:0 0 auto}
`
