package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/oblireach/supportlink"
)

func TestLinkErrKind(t *testing.T) {
	b := &linkBackend{}
	for _, c := range []struct {
		err  error
		kind string
	}{
		{&supportlink.UpgradeError{Side: supportlink.UpgradeClient, MinVersion: "1.2.0"}, ErrKindUpgrade},
		{&supportlink.UpgradeError{Side: supportlink.UpgradeServer}, ErrKindRefused},
		{supportlink.ErrUpgradeRequired, ErrKindUpgrade},
		{supportlink.ErrBanned, ErrKindRefused},
		{fmt.Errorf("%w: %w", supportlink.ErrUnreachable, errors.New("dial tcp: refused")), ErrKindUnreachable},
	} {
		if got := b.ErrKind(c.err); got != c.kind {
			t.Errorf("%v: %s, want %s", c.err, got, c.kind)
		}
	}
}
