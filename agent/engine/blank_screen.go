package engine

// ── Blank screen ("hide the remote screen") ─────────────────────────────────
//
// Like TeamViewer's "Show black screen": the remote computer's physical
// screens show a full-screen page — the Oblireach logo and "Oblireach session
// in progress — {operator} is working on your computer. Please wait." in the
// language of the session's Windows user — while the operator keeps seeing
// the real desktop.
//
// Protocol (feature "blank_screen", gated in init.features; init.blankScreen
// {available, reason} says why it is not offered):
//   viewer -> agent  {"type":"set_blank_screen","on":true|false}
//   agent  -> viewer {"type":"blank_screen_status","active":bool,"reason"?}
// reason (when active is false): operator | local_user | session_changed |
// os_version | no_user | failed | watchdog | unsupported.
//
// The operator's name is NEVER taken from the viewer: the service's relay
// reader (remote_session_windows.go) overwrites "operator" with the name the
// Obliance server put in open_remote_tunnel before the frame reaches the
// helper.
//
// Safety (Windows, blank_screen_windows.go): the page is shown only once
// SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE) is confirmed on every
// window (Windows 10 2004 / build 19041+); otherwise nothing is shown — never
// a black screen that would blind the operator too. The windows belong to the
// capture helper: they go with it (end of stream, viewer gone, service pipe
// broken, helper crash — Windows destroys a dead process's windows), and a
// watchdog removes them when the helper's capture loop stops beating.
//
// This file is PURE Go (no cgo, no build tag): texts, language choice,
// message framing — unit-tested everywhere (blank_screen_test.go).

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

const featBlankScreen = "blank_screen"

// blankScreenText is what the page shows.
type blankScreenText struct {
	Title string
	Body  string
	Hint  string
	RTL   bool
}

// blankScreenCatalog: title, body with {name}, body without a name, hint —
// the 18 languages of the Oblireach / Obliance UIs. Formal address.
var blankScreenCatalog = map[string][4]string{
	"en":    {"Oblireach session in progress", "{name} is working on your computer. Please wait.", "A technician is working on your computer. Please wait.", "Your screen will come back automatically when the session ends."},
	"fr":    {"Session Oblireach en cours", "{name} intervient sur votre ordinateur. Merci de patienter.", "Un technicien intervient sur votre ordinateur. Merci de patienter.", "Votre écran réapparaîtra automatiquement à la fin de l'intervention."},
	"de":    {"Oblireach-Sitzung läuft", "{name} arbeitet an Ihrem Computer. Bitte warten Sie.", "Ein Techniker arbeitet an Ihrem Computer. Bitte warten Sie.", "Ihr Bildschirm erscheint am Ende der Sitzung automatisch wieder."},
	"es":    {"Sesión de Oblireach en curso", "{name} está trabajando en su equipo. Espere, por favor.", "Un técnico está trabajando en su equipo. Espere, por favor.", "Su pantalla volverá a mostrarse automáticamente al finalizar la sesión."},
	"it":    {"Sessione Oblireach in corso", "{name} sta intervenendo sul suo computer. Attenda, per favore.", "Un tecnico sta intervenendo sul suo computer. Attenda, per favore.", "Il suo schermo tornerà visibile automaticamente al termine della sessione."},
	"pt-BR": {"Sessão Oblireach em andamento", "{name} está trabalhando no seu computador. Aguarde, por favor.", "Um técnico está trabalhando no seu computador. Aguarde, por favor.", "Sua tela voltará automaticamente ao final da sessão."},
	"nl":    {"Oblireach-sessie bezig", "{name} werkt aan uw computer. Even geduld alstublieft.", "Een technicus werkt aan uw computer. Even geduld alstublieft.", "Uw scherm verschijnt automatisch weer zodra de sessie is afgelopen."},
	"pl":    {"Trwa sesja Oblireach", "{name} pracuje na tym komputerze. Prosimy o cierpliwość.", "Technik pracuje na tym komputerze. Prosimy o cierpliwość.", "Ekran pojawi się ponownie automatycznie po zakończeniu sesji."},
	"cs":    {"Probíhá relace Oblireach", "{name} pracuje na vašem počítači. Vyčkejte prosím.", "Technik pracuje na vašem počítači. Vyčkejte prosím.", "Obrazovka se automaticky znovu zobrazí po skončení relace."},
	"da":    {"Oblireach-session i gang", "{name} arbejder på computeren. Vent venligst.", "En tekniker arbejder på computeren. Vent venligst.", "Skærmen vises automatisk igen, når sessionen er slut."},
	"sv":    {"Oblireach-session pågår", "{name} arbetar på datorn. Var god vänta.", "En tekniker arbetar på datorn. Var god vänta.", "Skärmen visas automatiskt igen när sessionen är slut."},
	"tr":    {"Oblireach oturumu sürüyor", "{name} bilgisayarınızda çalışıyor. Lütfen bekleyin.", "Bir teknisyen bilgisayarınızda çalışıyor. Lütfen bekleyin.", "Oturum sona erdiğinde ekranınız otomatik olarak geri gelecektir."},
	"ru":    {"Идёт сеанс Oblireach", "{name} работает на вашем компьютере. Пожалуйста, подождите.", "Специалист работает на вашем компьютере. Пожалуйста, подождите.", "Экран автоматически вернётся по окончании сеанса."},
	"uk":    {"Триває сеанс Oblireach", "{name} працює на вашому комп’ютері. Будь ласка, зачекайте.", "Фахівець працює на вашому комп’ютері. Будь ласка, зачекайте.", "Екран автоматично повернеться після завершення сеансу."},
	"ja":    {"Oblireach セッション実行中", "{name} がお使いのコンピューターで作業しています。しばらくお待ちください。", "技術担当者がお使いのコンピューターで作業しています。しばらくお待ちください。", "セッションが終了すると、画面は自動的に元に戻ります。"},
	"ko":    {"Oblireach 세션 진행 중", "{name} 님이 이 컴퓨터에서 작업 중입니다. 잠시 기다려 주십시오.", "기술 담당자가 이 컴퓨터에서 작업 중입니다. 잠시 기다려 주십시오.", "세션이 끝나면 화면이 자동으로 다시 표시됩니다."},
	"zh-CN": {"Oblireach 会话进行中", "{name} 正在操作您的计算机，请稍候。", "技术人员正在操作您的计算机，请稍候。", "会话结束后，屏幕将自动恢复。"},
	"ar":    {"جلسة Oblireach جارية", "يعمل {name} على جهاز الكمبيوتر الخاص بك. يُرجى الانتظار.", "يعمل أحد الفنيين على جهاز الكمبيوتر الخاص بك. يُرجى الانتظار.", "ستعود الشاشة تلقائيًا عند انتهاء الجلسة."},
}

// blankLangByPrimaryID maps a Windows PRIMARYLANGID to a catalog language.
var blankLangByPrimaryID = map[uint16]string{
	0x01: "ar", 0x04: "zh-CN", 0x05: "cs", 0x06: "da", 0x07: "de", 0x09: "en", 0x0a: "es", 0x0c: "fr",
	0x10: "it", 0x11: "ja", 0x12: "ko", 0x13: "nl", 0x15: "pl", 0x16: "pt-BR", 0x19: "ru", 0x1d: "sv",
	0x1f: "tr", 0x22: "uk",
}

// blankLangFromTag: a BCP-47 tag ("fr-FR", "pt-PT", "zh-Hans-CN") → a
// catalog language, "" when not in the catalog.
func blankLangFromTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(tag, "_", "-")))
	if tag == "" {
		return ""
	}
	primary := strings.SplitN(tag, "-", 2)[0]
	switch primary {
	case "pt":
		return "pt-BR"
	case "zh":
		return "zh-CN"
	}
	if _, ok := blankScreenCatalog[primary]; ok {
		return primary
	}
	return ""
}

// cleanOperatorName: the operator's name as shown to the local user — no
// control / bidi-override characters, one line, at most 64 runes.
func cleanOperatorName(s string) string {
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || (r >= 0x200b && r <= 0x200f) || (r >= 0x2028 && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0xfeff {
			r = ' '
		}
		if unicode.IsSpace(r) {
			if space || b.Len() == 0 {
				continue
			}
			space = true
			r = ' '
		} else {
			space = false
		}
		if n >= 64 {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// blankScreenTextFor: the page in `lang` (catalog language, English when
// unknown), naming `operator` when known.
func blankScreenTextFor(lang, operator string) blankScreenText {
	e, ok := blankScreenCatalog[lang]
	if !ok {
		lang = "en"
		e = blankScreenCatalog["en"]
	}
	op := cleanOperatorName(operator)
	body := e[2]
	if op != "" {
		body = strings.ReplaceAll(e[1], "{name}", op)
	}
	return blankScreenText{Title: e[0], Body: body, Hint: e[3], RTL: lang == "ar"}
}

// ── Messages ─────────────────────────────────────────────────────────────────

var blankScreenMark = []byte(`set_blank_screen`)

type blankScreenRequest struct {
	On       bool
	Operator string
}

// parseBlankScreenRequest: a viewer's set_blank_screen frame.
func parseBlankScreenRequest(payload []byte) (blankScreenRequest, bool) {
	if !bytes.Contains(payload, blankScreenMark) {
		return blankScreenRequest{}, false
	}
	var m struct {
		Type     string `json:"type"`
		On       bool   `json:"on"`
		Operator string `json:"operator"`
	}
	if json.Unmarshal(payload, &m) != nil || m.Type != "set_blank_screen" {
		return blankScreenRequest{}, false
	}
	return blankScreenRequest{On: m.On, Operator: m.Operator}, true
}

// IsBlankScreenMsg reports whether a viewer JSON frame is a set_blank_screen.
func IsBlankScreenMsg(payload []byte) bool {
	_, ok := parseBlankScreenRequest(payload)
	return ok
}

// WithBlankOperator rebuilds a set_blank_screen frame with the operator name
// the SERVICE knows (whatever the viewer put there is dropped).
func WithBlankOperator(payload []byte, operator string) []byte {
	req, _ := parseBlankScreenRequest(payload)
	b, _ := json.Marshal(map[string]interface{}{"type": "set_blank_screen", "on": req.On, "operator": cleanOperatorName(operator)})
	return b
}

// BlankScreenStatusJSON: the agent's answer (the state actually applied).
func BlankScreenStatusJSON(active bool, reason string) []byte {
	m := map[string]interface{}{"type": "blank_screen_status", "active": active}
	if !active && reason != "" {
		m["reason"] = reason
	}
	b, _ := json.Marshal(m)
	return b
}

// blankScreenInitInfo is init.blankScreen of the helper.
func blankScreenInitInfo() map[string]interface{} {
	ok, reason := blankScreenAvailable()
	if ok {
		return map[string]interface{}{"available": true}
	}
	return map[string]interface{}{"available": false, "reason": reason}
}

// ── Watchdog heartbeat ───────────────────────────────────────────────────────
// The capture loop of the helper beats (blankScreenBeat) on every tick; the
// window thread removes the page when no beat came for blankWatchdogMs — a
// helper whose capture loop hangs never leaves the local user in front of a
// page with nobody working behind it.

const blankWatchdogMs = 10_000

var blankLastBeat atomic.Int64

func blankScreenBeat() { blankLastBeat.Store(time.Now().UnixMilli()) }

// blankBeatStale: the capture loop did not beat for blankWatchdogMs.
func blankBeatStale(now time.Time) bool {
	last := blankLastBeat.Load()
	return last == 0 || now.UnixMilli()-last > blankWatchdogMs
}

// blankScreenAutoOff carries a removal the platform did by itself (watchdog,
// display failure) to the capture loop, which tells the viewer.
var blankScreenAutoOff = make(chan string, 1)

func notifyBlankAutoOff(reason string) {
	select {
	case blankScreenAutoOff <- reason:
	default:
	}
}

// ── State (one per capture helper: one viewer) ───────────────────────────────

var blankActive atomic.Bool

// applyBlankScreen shows or removes the page and returns the status to send.
func applyBlankScreen(req blankScreenRequest) []byte {
	if !req.On {
		if blankActive.Load() {
			platformBlankScreenHide()
			blankActive.Store(false)
		}
		return BlankScreenStatusJSON(false, "operator")
	}
	if ok, reason := blankScreenAvailable(); !ok {
		return BlankScreenStatusJSON(false, reason)
	}
	blankScreenBeat()
	if reason := platformBlankScreenShow(blankScreenTextFor(platformBlankScreenLang(), req.Operator)); reason != "" {
		blankActive.Store(false)
		return BlankScreenStatusJSON(false, reason)
	}
	blankActive.Store(true)
	return BlankScreenStatusJSON(true, "")
}

// releaseBlankScreen: the stream ends (every exit of the capture loop).
func releaseBlankScreen() {
	if blankActive.Swap(false) {
		platformBlankScreenHide()
	}
}

// blankScreenAvailable: whether this build, on this Windows, can show the page
// safely; else the reason sent to the viewers.
func blankScreenAvailable() (bool, string) { return platformBlankScreenAvailable() }
