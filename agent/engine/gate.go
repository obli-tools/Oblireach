package engine

// The gate: the policy layer between a session (RunSession) and its
// FrameConn. Every frame goes through it, both directions:
//   - viewer → host: what Options does not allow never reaches the capture
//     side (input, clipboard, files, blank screen, local input block, display
//     modes; type_text always dropped); the requests a viewer waits on are
//     answered with the same refusal messages the agent uses; chat lines go to
//     Options.OnChat;
//   - host → viewer: the init message advertises only what is allowed (its
//     features list and capability objects are rewritten), and messages of a
//     disallowed feature (clipboard content, file offers, the vault's input
//     desktop) are dropped.
// It also serialises writes (the capture loop writes from several
// goroutines) and fires Options.OnStart on the first init.
//
// The agent service never uses the gate: its own relay applies the Obliance
// server's decisions (files_gate.go, remote_session_windows.go).

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

type gate struct {
	conn    FrameConn
	opts    Options
	wmu     sync.Mutex
	started sync.Once
}

func newGate(conn FrameConn, opts Options) *gate {
	return &gate{conn: conn, opts: opts}
}

func (g *gate) Close() error { return g.conn.Close() }

// write sends a frame as is (serialised).
func (g *gate) write(op byte, p []byte) error {
	g.wmu.Lock()
	defer g.wmu.Unlock()
	return g.conn.WriteFrame(op, p)
}

// WriteFrame filters and sends one host → viewer frame. A dropped frame is
// not an error.
func (g *gate) WriteFrame(op byte, p []byte) error {
	isInit := false
	switch op {
	case 0x1:
		var keep bool
		p, keep, isInit = g.outText(p)
		if !keep {
			return nil
		}
	case 0x2:
		if !g.opts.AllowFiles && len(p) > 0 && p[0] == frameTypeFileDown {
			return nil
		}
	}
	if err := g.write(op, p); err != nil {
		return err
	}
	if isInit && g.opts.OnStart != nil {
		g.started.Do(g.opts.OnStart)
	}
	return nil
}

// ReadFrame returns the next viewer → host frame the session may see,
// answering or dropping the others.
func (g *gate) ReadFrame() (byte, []byte, error) {
	for {
		op, p, err := g.conn.ReadFrame()
		if err != nil {
			return op, p, err
		}
		switch op {
		case 0x1:
			reply, pass := g.inText(p)
			if reply != nil {
				_ = g.write(0x1, reply)
			}
			if !pass {
				continue
			}
		case 0x2:
			if !g.opts.AllowFiles {
				continue // the only viewer binary frames are file uploads (0x20)
			}
		}
		return op, p, nil
	}
}

// inText decides on one viewer JSON message: pass it on, or not, and the
// reply to send back (nil = none).
func (g *gate) inText(p []byte) (reply []byte, pass bool) {
	o := g.opts
	t := controlMsgType(p)
	switch {
	case t == "chat":
		if o.OnChat != nil {
			var m struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(p, &m) == nil && m.Text != "" {
				o.OnChat(cutUTF8(m.Text, ChatMaxBytes))
			}
		}
		return nil, false
	case t == "type_text":
		// The agent's password vault: never through this entry point, and
		// the secret is wiped before anything else sees the buffer.
		WipeBytes(p)
		return nil, false
	case t == "mouse" || t == "key" || t == "sas":
		return nil, o.AllowInput
	case t == "set_input_block":
		if o.AllowInput && o.AllowInputBlock {
			return nil, true
		}
		b, _ := json.Marshal(map[string]interface{}{"type": "input_block_status", "blocked": false})
		return b, false
	case t == "set_blank_screen":
		if o.AllowBlankScreen {
			return nil, true
		}
		return BlankScreenStatusJSON(false, "unsupported"), false
	case t == "display_modes_get" || t == "display_mode_set":
		if o.AllowDisplayModes {
			return nil, true
		}
		return DisplayRefusal(p, "unsupported"), false
	case strings.HasPrefix(t, "clipboard_"):
		return nil, o.AllowClipboard
	case strings.HasPrefix(t, "file_"):
		if o.AllowFiles {
			return nil, true
		}
		return FileRefusal(p), false
	}
	return nil, true
}

// outText decides on one host → viewer JSON message: the (possibly
// rewritten) payload, whether to send it, whether it is an init.
func (g *gate) outText(p []byte) ([]byte, bool, bool) {
	o := g.opts
	t := controlMsgType(p)
	switch {
	case t == "init":
		return g.rewriteInit(p), true, true
	case t == "input_desktop" || t == "type_text_result":
		return p, false, false // the vault's: never offered here
	case strings.HasPrefix(t, "clipboard_"):
		if !o.AllowClipboard || (t == "clipboard_files" && !o.AllowFiles) {
			return p, false, false
		}
	case !o.AllowFiles && IsAgentFileMessage(p):
		return p, false, false
	}
	return p, true, false
}

// featureAllowed: whether an init.features entry stays advertised.
func (g *gate) featureAllowed(f string) bool {
	o := g.opts
	switch f {
	case featTypeText, featInputDesktop, featTypeTextSAS, featSessionFollow:
		return false
	case featKeyMapMac:
		return o.AllowInput
	case featInputBlockLocal:
		return o.AllowInput && o.AllowInputBlock
	case featClipboardText, featClipboardPush:
		return o.AllowClipboard
	case featClipboardFiles:
		return o.AllowClipboard && o.AllowFiles
	case featFileUpload, featFileDownload:
		return o.AllowFiles
	case featBlankScreen:
		return o.AllowBlankScreen
	case featDisplayModes:
		return o.AllowDisplayModes
	}
	return true
}

// rewriteInit makes an init advertise only what Options allow. Numbers keep
// their exact text (json.Number). An init that does not decode goes out as is.
func (g *gate) rewriteInit(p []byte) []byte {
	o := g.opts
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber()
	var m map[string]interface{}
	if dec.Decode(&m) != nil {
		return p
	}
	feats := []string{}
	if list, ok := m["features"].([]interface{}); ok {
		for _, v := range list {
			if f, ok := v.(string); ok && g.featureAllowed(f) {
				feats = append(feats, f)
			}
		}
	}
	if o.OnChat != nil {
		feats = append(feats, featChat)
	}
	m["features"] = feats
	if c, ok := m["clipboard"].(map[string]interface{}); ok {
		if !o.AllowClipboard {
			c["text"], c["push"] = false, false
		}
		if !o.AllowClipboard || !o.AllowFiles {
			c["files"] = false
		}
	}
	if f, ok := m["files"].(map[string]interface{}); ok && !o.AllowFiles {
		f["upload"], f["download"] = false, false
	}
	if !o.AllowBlankScreen {
		m["blankScreen"] = map[string]interface{}{"available": false, "reason": "unsupported"}
	}
	// The vault's context (input desktop, domain membership): not offered.
	delete(m, "inputDesktop")
	delete(m, "domain")
	b, err := json.Marshal(m)
	if err != nil {
		return p
	}
	return b
}
