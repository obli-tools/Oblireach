// Command linkpeer is one side of the cross-line tests (interop_test.go in
// supportlink): a host or an operator that runs interop.Run over a real
// session. The tests build it with Go 1.20 and the nt6 tag (pion/webrtc
// v3) and pair it with the modern line in the test process:
//
//	C:\Go1.20\bin\go.exe build -tags nt6 -modfile=go120.mod -o linkpeer-nt6.exe ./internal/interop/linkpeer
//
// stdout, one line each:
//
//	WAITING <id> <code>                  host: registered
//	SESSION <path> <sas> <detail...>     the link is up
//	REPORT <text>                        the exchange passed
//	ERROR <text>                         and exit status 1
//
// A host exits when the operator closes the session; an operator closes it
// after the exchange (and -hold).
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/internal/interop"
)

func main() {
	role := flag.String("role", "host", "host or operator")
	server := flag.String("server", "", "rendezvous URL (ws:// or wss://)")
	key := flag.String("key", "", "operator key")
	id := flag.String("id", "", "operator: host ID")
	code := flag.String("code", "", "operator: one-time code")
	relay := flag.Bool("relay", false, "relay only")
	tcp := flag.Bool("tcp", false, "TURN over TCP / TLS only")
	tlsOnly := flag.Bool("tls", false, "TURN over TLS only")
	ca := flag.String("ca", "", "PEM file of the root to trust (wss:// and turns:)")
	n := flag.Int("n", 300, "frames each way")
	hold := flag.Duration("hold", 0, "operator: keep the session this long after the exchange")
	flag.Parse()

	ice := supportlink.ICEOptions{
		IncludeLoopback: true, IPFilter: func(ip net.IP) bool { return ip.IsLoopback() },
		ForceRelay: *relay, TURNTCPOnly: *tcp, TURNTLSOnly: *tlsOnly,
	}
	so := supportlink.SignalOptions{Proxy: supportlink.ProxyDirect}
	if *ca != "" {
		pem, err := os.ReadFile(*ca)
		if err != nil {
			fail(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			fail(fmt.Errorf("no certificate in %s", *ca))
		}
		so.TLSConfig = &tls.Config{RootCAs: pool}
	}
	var s *supportlink.Session
	switch *role {
	case "host":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		h, err := supportlink.Register(ctx, supportlink.HostConfig{
			Server: *server, ClientVersion: "1.0.0", ICE: ice, SignalOptions: so,
			Approve: func(context.Context, supportlink.ApprovalRequest) bool { return true },
		})
		cancel()
		if err != nil {
			fail(err)
		}
		defer h.Close()
		fmt.Printf("WAITING %s %s\n", h.ID(), h.Code())
		ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
		s, err = h.WaitForOperator(ctx)
		cancel()
		if err != nil {
			fail(err)
		}
	case "operator":
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		var err error
		s, err = supportlink.Connect(ctx, supportlink.OperatorConfig{
			Server: *server, ClientVersion: "1.0.0", Key: *key, Name: "nt6 operator", ICE: ice, SignalOptions: so,
		}, *id, *code)
		cancel()
		if err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown role %q", *role))
	}
	fmt.Printf("SESSION %s %s %s\n", s.Path(), strings.ReplaceAll(s.SAS(), " ", "_"), s.PathDetail())
	rep, err := interop.Run(s, *role == "host", *n)
	if err != nil {
		s.Close()
		fail(err)
	}
	fmt.Printf("REPORT %s\n", rep)
	if *role == "operator" {
		time.Sleep(*hold)
		s.Close()
		return
	}
	select {
	case <-s.Done():
	case <-time.After(30 * time.Second):
		fail(fmt.Errorf("the operator did not close the session"))
	}
}

func fail(err error) {
	fmt.Printf("ERROR %v\n", err)
	os.Exit(1)
}
