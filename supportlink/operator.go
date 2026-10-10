package supportlink

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"filippo.io/cpace"

	"github.com/oblireach/supportlink/wire"
)

// OperatorConfig configures the helping side.
type OperatorConfig struct {
	// Server is the rendezvous server the host registered on.
	Server string
	SignalOptions
	ClientVersion string

	// Key is a static operator key of the instance. Token is the login
	// token of an operator account (oblireach-rdv POST /v1/account/login).
	// Every operator needs one or the other, direct path included; when
	// both are set the server uses the token if it is valid. An expired or
	// revoked token fails with ErrAuthExpired (log in again).
	Key   string
	Token string
	// Name is shown to the person in the acceptance window, flagged as
	// self-declared next to the server-authenticated account.
	Name string

	// OnAwaitingApproval is called once the code is verified, while the
	// person decides; sas is the verification code shown on both sides.
	OnAwaitingApproval func(sas string)

	ICE ICEOptions
	// Logger is a *log/slog.Logger (golang.org/x/exp/slog on the nt6
	// line, Go 1.20); nil discards.
	Logger *slogLogger
}

// Connect opens a session to host id with the one-time code. It returns once
// the person accepted and the peer-to-peer link is up. ctx bounds the whole
// setup (the person's decision included); the returned Session outlives it.
func Connect(ctx context.Context, cfg OperatorConfig, id, code string) (*Session, error) {
	id, code = wire.Digits(id), wire.Digits(code)
	if !wire.ValidID(id) {
		return nil, ErrInvalidID
	}
	if !wire.ValidCode(code) {
		return nil, ErrInvalidCode
	}
	if cfg.ICE.ConnectTimeout <= 0 {
		cfg.ICE.ConnectTimeout = 30 * time.Second
	}
	log := logger(cfg.Logger)
	sc, err := dialSignal(ctx, cfg.Server, cfg.SignalOptions, wire.Msg{Role: wire.RoleOperator, Ver: cfg.ClientVersion})
	if err != nil {
		return nil, err
	}
	op := &opFlow{cfg: cfg, log: log, sig: sc, id: id, code: code, opened: make(chan error, 1)}
	sess, err := op.run(ctx)
	if err != nil {
		if op.sid != "" {
			_ = sc.send(wire.Msg{T: wire.TBye, SID: op.sid, Reason: byeReason(err)})
		}
		if op.sess != nil {
			op.sess.closeWith(true, err)
		} else if op.peer != nil {
			_ = op.peer.close()
		}
		sc.close()
		return nil, err
	}
	return sess, nil
}

type opFlow struct {
	cfg  OperatorConfig
	log  *slogLogger
	sig  *sigConn
	id   string
	code string

	sid       string
	st        *cpace.State
	keys      sessionKeys
	verified  bool
	ice       *wire.ICEInfo
	peer      rtcPeer
	sess      *Session
	remoteSet bool
	queued    []string
	opened    chan error
}

func byeReason(err error) string {
	switch {
	case errors.Is(err, ErrMITM):
		return "fingerprint_mismatch"
	case errors.Is(err, ErrWrongCode):
		return "bad_code"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return "setup_failed"
	}
}

func (o *opFlow) run(ctx context.Context) (*Session, error) {
	if err := o.sig.send(wire.Msg{T: wire.TConnect, ID: o.id, Key: o.cfg.Key, Token: o.cfg.Token}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-o.opened:
			if err != nil {
				return nil, err
			}
			return o.established(), nil
		case m, ok := <-o.sig.in:
			if !ok {
				return nil, fmt.Errorf("%w: signalling connection lost", ErrUnreachable)
			}
			if m.SID != "" && o.sid != "" && m.SID != o.sid {
				continue
			}
			if err := o.handle(m); err != nil {
				return nil, err
			}
		}
	}
}

func (o *opFlow) handle(m wire.Msg) error {
	switch m.T {
	case wire.TSession:
		if o.sid != "" || m.SID == "" {
			return nil
		}
		o.sid = m.SID
		msgA, st, err := operatorPakeStart(o.code, o.id, o.sid)
		if err != nil {
			return err
		}
		o.st = st
		return o.sig.send(wire.Msg{T: wire.TPake, SID: o.sid, Pake: base64.StdEncoding.EncodeToString(msgA)})

	case wire.TPakeReply:
		if o.st == nil || o.verified {
			return nil
		}
		msgB, err := base64.StdEncoding.DecodeString(m.Pake)
		if err != nil {
			return ErrWrongCode
		}
		key, err := o.st.Finish(msgB)
		if err != nil {
			return ErrWrongCode
		}
		o.keys = deriveKeys(key)
		if !checkMAC(o.keys.confirmHost, hostConfirmData(o.sid), m.Conf) {
			// Wrong code: the bye sent by Connect costs this attempt on the
			// server, and the host burns its code.
			return ErrWrongCode
		}
		o.verified = true
		if err := o.sig.send(wire.Msg{T: wire.TConfOp, SID: o.sid, Name: cleanName(o.cfg.Name),
			Conf: macB64(o.keys.confirmOp, opConfirmData(o.sid))}); err != nil {
			return err
		}
		if o.cfg.OnAwaitingApproval != nil {
			o.cfg.OnAwaitingApproval(o.keys.sas)
		}

	case wire.TAccept:
		if !o.verified || o.peer != nil {
			return nil
		}
		o.ice = m.ICE
		peer, err := newRTCPeer(o.ice, o.cfg.ICE, o.cfg.SignalOptions)
		if err != nil {
			return err
		}
		o.peer = peer
		sid, keys := o.sid, o.keys
		peer.onCandidate(func(c string) {
			box, err := seal(keys.boxOp, sid, wire.TCand, []byte(c))
			if err == nil {
				_ = o.sig.send(wire.Msg{T: wire.TCand, SID: sid, Box: box})
			}
		})
		o.sess = newSession(sid, keys, PeerInfo{}, peer)

	case wire.TOffer:
		if o.sess == nil || o.remoteSet {
			return nil
		}
		sdp, err := openSDP(o.keys.boxHost, o.keys.fpHost, o.sid, wire.TOffer, m.Box, m.MAC)
		if err != nil {
			return err
		}
		ans, err := o.peer.acceptOffer(sdp)
		if err != nil {
			return err
		}
		box, mac, err := sealSDP(o.keys.boxOp, o.keys.fpOp, o.sid, wire.TAnswer, ans)
		if err != nil {
			return err
		}
		if err := o.sig.send(wire.Msg{T: wire.TAnswer, SID: o.sid, Box: box, MAC: mac}); err != nil {
			return err
		}
		o.remoteSet = true
		for _, c := range o.queued {
			_ = o.peer.addCandidate(c)
		}
		o.queued = nil
		s := o.sess
		go func() { o.opened <- s.waitOpen(o.cfg.ICE.ConnectTimeout) }()

	case wire.TCand:
		if o.sess == nil {
			return nil
		}
		c, err := open(o.keys.boxHost, o.sid, wire.TCand, m.Box)
		if err != nil {
			return fmt.Errorf("%w: candidate %v", ErrMITM, err)
		}
		if o.remoteSet {
			_ = o.peer.addCandidate(string(c))
		} else {
			o.queued = append(o.queued, string(c))
		}

	case wire.TReject:
		return ErrRejected

	case wire.TBye:
		if m.Reason == "fingerprint_mismatch" {
			return fmt.Errorf("%w: reported by the host", ErrMITM)
		}
		// The host left (or its connection dropped), possibly in the middle
		// of the PAKE. That is not a wrong code: a wrong code is reported by
		// the server (error bad_code, after the host's pake_failed) or
		// detected here on the host's key confirmation.
		return ErrPeerGone

	case wire.TError:
		return serverError(m)
	}
	return nil
}

// established hands the session over: a goroutine keeps the signalling link
// for late candidates and the peer's bye, and the session's close sends ours.
func (o *opFlow) established() *Session {
	s := o.sess
	_ = o.sig.send(wire.Msg{T: wire.TPath, SID: o.sid, Path: string(s.Path())})
	s.setOnClosed(func(local bool, err error) {
		if local {
			_ = o.sig.send(wire.Msg{T: wire.TBye, SID: o.sid})
		}
		o.sig.close()
	})
	go func() {
		for m := range o.sig.in {
			if m.SID != o.sid {
				continue
			}
			switch m.T {
			case wire.TCand:
				if c, err := open(o.keys.boxHost, o.sid, wire.TCand, m.Box); err == nil {
					_ = o.peer.addCandidate(string(c))
				}
			case wire.TBye, wire.TError:
				s.closeWith(false, io.EOF)
			}
		}
	}()
	return s
}
