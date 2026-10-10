package supportlink

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oblireach/supportlink/wire"
)

func TestUpgradeErrorSides(t *testing.T) {
	for _, c := range []struct {
		m    wire.Msg
		side UpgradeSide
		min  string
	}{
		{wire.Msg{Err: wire.ErrUpgradeRequired}, UpgradeServer, ""}, // server older than protocol 2
		{wire.Msg{Err: wire.ErrUpgradeRequired, Proto: wire.ProtocolVersion - 1, Reason: "1.0.0"}, UpgradeServer, "1.0.0"},
		{wire.Msg{Err: wire.ErrUpgradeRequired, Proto: wire.ProtocolVersion, Reason: "1.2.0"}, UpgradeClient, "1.2.0"},
		{wire.Msg{Err: wire.ErrUpgradeRequired, Proto: wire.ProtocolVersion + 1}, UpgradeClient, ""},
		{wire.Msg{Err: wire.ErrUpgradeRequired, Proto: wire.ProtocolVersion, Reason: "1.2.0<script>"}, UpgradeClient, ""},
	} {
		err := serverError(c.m)
		var ue *UpgradeError
		if !errors.As(err, &ue) || !errors.Is(err, ErrUpgradeRequired) {
			t.Fatalf("%+v: %v is not an UpgradeError matching ErrUpgradeRequired", c.m, err)
		}
		if ue.Side != c.side || ue.MinVersion != c.min {
			t.Errorf("%+v: side %s min %q, want %s %q", c.m, ue.Side, ue.MinVersion, c.side, c.min)
		}
		if errors.Is(err, ErrBusy) {
			t.Error("an UpgradeError must match only ErrUpgradeRequired")
		}
	}
	if err := serverError(wire.Msg{Err: wire.ErrBusy}); errors.Is(err, ErrUpgradeRequired) {
		t.Error("busy matched ErrUpgradeRequired")
	}
}

// TestOperatorByeDuringPake: a bye from the host's side in the middle of the
// PAKE means the person left, not a wrong code.
func TestOperatorByeDuringPake(t *testing.T) {
	_, st, err := operatorPakeStart("123456789", "482913507", "sid")
	if err != nil {
		t.Fatal(err)
	}
	o := &opFlow{sid: "sid", st: st}
	for _, reason := range []string{"", "peer_disconnected", "bad_code"} {
		err := o.handle(wire.Msg{T: wire.TBye, SID: "sid", Reason: reason})
		if !errors.Is(err, ErrPeerGone) || errors.Is(err, ErrWrongCode) {
			t.Errorf("bye %q during the PAKE: %v", reason, err)
		}
	}
	// The server's verdict on a wrong code stays a wrong code.
	if err := o.handle(wire.Msg{T: wire.TError, SID: "sid", Err: wire.ErrBadCode}); !errors.Is(err, ErrWrongCode) {
		t.Errorf("bad_code: %v", err)
	}
}

func TestSignallingErrors(t *testing.T) {
	// A port nobody listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "ws://" + l.Addr().String()
	l.Close()

	// A server that accepts the WebSocket then hangs up before answering.
	up := websocket.Upgrader{}
	hangup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		var m wire.Msg
		_ = c.ReadJSON(&m) // hello
		c.Close()
	}))
	defer hangup.Close()
	// An HTTP server that is not a rendezvous server (no WebSocket).
	notWS := httptest.NewServer(http.NotFoundHandler())
	defer notWS.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	op := func(server string) error {
		_, err := Connect(ctx, OperatorConfig{Server: server, Key: "k"}, "482913507", "123456789")
		return err
	}
	host := func(server string) error {
		_, err := Register(ctx, HostConfig{Server: server, Approve: func(context.Context, ApprovalRequest) bool { return false }})
		return err
	}
	for name, server := range map[string]string{
		"refused": dead,
		"hangup":  "ws://" + strings.TrimPrefix(hangup.URL, "http://"),
		"not ws":  "ws://" + strings.TrimPrefix(notWS.URL, "http://"),
	} {
		if err := op(server); !errors.Is(err, ErrUnreachable) {
			t.Errorf("operator, %s: want ErrUnreachable, got %v", name, err)
		}
		if err := host(server); !errors.Is(err, ErrUnreachable) {
			t.Errorf("host, %s: want ErrUnreachable, got %v", name, err)
		}
	}
	for _, server := range []string{"", "ftp://example.com", "ws://example.com", "wss://exa mple.com/%zz"} {
		if err := op(server); !errors.Is(err, ErrInvalidServer) || errors.Is(err, ErrUnreachable) {
			t.Errorf("operator, %q: want ErrInvalidServer, got %v", server, err)
		}
	}
	// A cancelled setup is a cancellation, not an unreachable server.
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, err := Connect(cctx, OperatorConfig{Server: dead, Key: "k"}, "482913507", "123456789"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnreachable) {
		t.Errorf("cancelled: %v", err)
	}
}
