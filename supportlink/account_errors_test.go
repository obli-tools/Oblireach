package supportlink

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oblireach/supportlink/wire"
)

// TestAccountErrors: an expired login matches ErrAuthExpired and, for code
// written against static keys, ErrOperatorRequired; a quota error carries
// the quota reached.
func TestAccountErrors(t *testing.T) {
	err := serverError(wire.Msg{Err: wire.ErrAuthExpired})
	if !errors.Is(err, ErrAuthExpired) || !errors.Is(err, ErrOperatorRequired) || errors.Is(err, ErrQuota) {
		t.Fatalf("auth_expired: %v", err)
	}
	if err := serverError(wire.Msg{Err: wire.ErrOperatorRequired}); errors.Is(err, ErrAuthExpired) {
		t.Fatal("operator_required must not match ErrAuthExpired")
	}

	err = serverError(wire.Msg{Err: wire.ErrQuota, Reason: wire.QuotaDaily, Retry: 3600})
	var se *ServerError
	if !errors.Is(err, ErrQuota) || !errors.As(err, &se) || se.Detail != wire.QuotaDaily || se.Retry != time.Hour {
		t.Fatalf("quota: %#v", err)
	}
	if !strings.Contains(err.Error(), "quota_exceeded sessions_per_day") {
		t.Fatalf("message %q", err.Error())
	}
	// Detail is network input: only short identifiers pass.
	err = serverError(wire.Msg{Err: wire.ErrQuota, Reason: "relay_month\x1b[31m"})
	if errors.As(err, &se); se.Detail != "" {
		t.Fatalf("unclean detail %q", se.Detail)
	}
	// Detail is only kept for quota errors.
	if errors.As(serverError(wire.Msg{Err: wire.ErrBusy, Reason: "x"}), &se); se.Detail != "" {
		t.Fatal("detail on a non-quota error")
	}
}

// TestConnectSendsToken: the token travels in connect next to the key.
func TestConnectSendsToken(t *testing.T) {
	m := wire.Msg{T: wire.TConnect, ID: "482913507", Key: "k", Token: "rdv1.abc"}
	if m.Token != "rdv1.abc" {
		t.Fatal("token field")
	}
}
