//go:build windows && e2ehost

package main

// Test host for end-to-end tests on a real machine (client-app
// TestSupportEndToEndRealEngine): the thin client's controller with the
// production backend (supportlink + the ObliReach engine capturing the real
// screen, configured as in main) and a view that accepts every request with
// the permissions set on stdin. No window.
//
// Built only with the e2ehost tag, never part of the released client (it
// accepts every request without asking anyone):
//
//	go build -tags e2ehost -o support-e2e-host.exe .
//	support-e2e-host.exe http://127.0.0.1:PORT
//
// stdout, one event per line:
//
//	WAITING <id> <code>        registered, waiting for an operator
//	SESSION <path> <relayed>   a session started
//	FILE <path>                the operator sent a file (Options.OnFileReceived)
//	CHAT <json string>         a chat line from the operator (sanitised)
//	ENDED <reason>             local | peer | lost | error[: message]
//	DIAG <lines> <input> <encoder>   engine diagnostic lines routed to the host (on quit)
//
// stdin, one command per line:
//
//	perms [control] [clipboard] [files]   permissions of the next acceptance (default: control clipboard)
//	stop                                  end the running session (the person's Stop button)
//	chat <text>                           send a chat line to the operator
//	quit                                  unregister and exit
//
// It replaces main: init runs the host and exits.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oblireach/agent/engine"
)

func init() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: support-e2e-host <rendezvous server URL>")
		os.Exit(2)
	}
	os.Exit(e2eHost(os.Args[1]))
}

// e2eView accepts every request with the current permissions and prints the
// state changes.
type e2eView struct {
	mu     sync.Mutex
	perms  Permissions
	out    *bufio.Writer
	lastID string
	lastPh Phase
	files  int
	chats  int // transcript lines printed
	ctl    *Controller
}

func (v *e2eView) println(s string) {
	v.out.WriteString(s + "\n")
	v.out.Flush()
}

func (v *e2eView) Show(s State) {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch {
	case s.Phase == PhaseWaiting && s.ID != "" && (s.ID+s.Code != v.lastID || v.lastPh != PhaseWaiting):
		v.lastID = s.ID + s.Code
		v.println(fmt.Sprintf("WAITING %s %s", s.ID, s.Code))
	case s.Phase == PhaseSession && v.lastPh != PhaseSession:
		v.files, v.chats = 0, 0
		v.println(fmt.Sprintf("SESSION %s %v", s.Session.Path, s.Session.Relayed))
	case s.Phase == PhaseEnded && v.lastPh != PhaseEnded:
		reason := [...]string{"local", "peer", "lost", "error"}[s.Ended.Reason]
		if s.Ended.Err != nil {
			reason += ": " + s.Ended.Err.Error()
		}
		v.println("ENDED " + reason)
		go v.ctl.Acknowledge()
	}
	if s.Phase == PhaseSession && s.Session.FilesReceived > v.files {
		v.files = s.Session.FilesReceived
		v.println("FILE " + s.Session.LastFileDir + `\` + s.Session.LastFile)
	}
	if s.Phase == PhaseSession {
		for _, m := range s.Session.Chat {
			if m.ID <= v.chats {
				continue
			}
			v.chats = m.ID
			if m.From == ChatFromOperator {
				q, _ := json.Marshal(m.Text)
				v.println("CHAT " + string(q))
			}
		}
	}
	v.lastPh = s.Phase
}

func (v *e2eView) Ask(ctx context.Context, a Approval) (Permissions, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.perms, true
}

func e2eHost(server string) int {
	hardenDLLSearch()
	configureEngine()
	engine.EnablePerMonitorDPIAwareness()
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	diag := &e2eDiag{}
	engine.SetDiagnosticOutput(diag)

	v := &e2eView{perms: DefaultPermissions(), out: bufio.NewWriter(os.Stdout)}
	c := NewController(&linkBackend{server: server, version: "e2e-host"}, v, log.Printf)
	v.ctl = c
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "perms":
			var p Permissions
			for _, x := range f[1:] {
				switch x {
				case "control":
					p.Control = true
				case "clipboard":
					p.Clipboard = true
				case "files":
					p.Files = true
				}
			}
			v.mu.Lock()
			v.perms = p
			v.mu.Unlock()
		case "stop":
			c.StopSession()
		case "chat":
			if err := c.SendChat(strings.TrimSpace(strings.TrimPrefix(sc.Text(), "chat"))); err != nil {
				log.Printf("chat: %v", err)
			}
		case "quit":
			goto quit
		}
	}
quit:
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	lines, input, enc := diag.counts()
	v.mu.Lock()
	v.println(fmt.Sprintf("DIAG %d %d %d", lines, input, enc))
	v.mu.Unlock()
	return 0
}

// e2eDiag counts the engine diagnostic lines (they also go to stderr).
type e2eDiag struct {
	mu                sync.Mutex
	lines, input, enc int
}

func (d *e2eDiag) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := string(p)
	d.lines += strings.Count(s, "\n")
	d.input += strings.Count(s, "[engine input]")
	d.enc += strings.Count(s, "[engine encoder]")
	return os.Stderr.Write(p)
}

func (d *e2eDiag) counts() (int, int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lines, d.input, d.enc
}
