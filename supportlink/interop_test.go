//go:build !nt6

package supportlink_test

// Cross-line tests: the nt6 line (Go 1.20, pion/webrtc v3, the linkpeer
// helper built with C:\Go1.20\bin\go.exe -tags nt6 -modfile=go120.mod) paired
// with the modern line (this test process, pion/webrtc v4) through a real
// oblireach-rdv built from ../rdv, both ways (nt6 host / modern operator and
// the reverse), on every path: direct, relay over UDP, relay over TCP (TLS
// off) and relay over TLS (TURN-TLS on the signalling port, self-signed
// certificate). Each pairing runs interop.Run: chat both ways, control round
// trips, and 300 frames each way at once checked for order and content; it
// also checks that the unused relay allocations are released.
//
// Builds two binaries and runs 8 sessions, so it only runs on demand:
//
//	SUPPORTLINK_INTEROP=1 go test -run TestCrossLine -v .
//
// OBLIREACH_GO120 overrides the Go 1.20 toolchain (default C:\Go1.20\bin\go.exe).

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sl "github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/internal/interop"
)

const (
	xKey    = "interop-operator-key-0123456789abcdef"
	xFrames = 300
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type xBins struct{ rdv, peer string }

// buildBins builds the rendezvous server (current toolchain) and the nt6
// linkpeer (Go 1.20).
func buildBins(t *testing.T) xBins {
	t.Helper()
	go120 := os.Getenv("OBLIREACH_GO120")
	if go120 == "" {
		go120 = `C:\Go1.20\bin\go.exe`
	}
	if _, err := os.Stat(go120); err != nil {
		t.Skipf("Go 1.20 toolchain not found (%s): %v", go120, err)
	}
	dir := t.TempDir()
	b := xBins{rdv: filepath.Join(dir, "rdv.exe"), peer: filepath.Join(dir, "linkpeer-nt6.exe")}
	cmd := exec.Command("go", "build", "-o", b.rdv, "./cmd/oblireach-rdv")
	cmd.Dir = filepath.Join("..", "rdv")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build rdv: %v\n%s", err, out)
	}
	cmd = exec.Command(go120, "build", "-tags", "nt6", "-modfile=go120.mod", "-o", b.peer, "./internal/interop/linkpeer")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build nt6 linkpeer: %v\n%s", err, out)
	}
	if v, err := exec.Command(go120, "version").Output(); err == nil {
		t.Logf("nt6 side: %s", strings.TrimSpace(string(v)))
	}
	return b
}

type xRDV struct {
	url, admin, ca string
	tlsConf        *tls.Config
}

func (r *xRDV) metric(t *testing.T, name string) int {
	t.Helper()
	resp, err := http.Get(r.admin + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, name+" ") {
			v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(l, name+" ")), 64)
			return int(v)
		}
	}
	return -1
}

var relayBlock struct {
	sync.Mutex
	n int
}

// startRDV runs the rendezvous server, TLS off or with a self-signed
// certificate for 127.0.0.1 (then TURN-TLS on the same port).
func startRDV(t *testing.T, bin string, withTLS bool) *xRDV {
	t.Helper()
	dir := t.TempDir()
	listen, admin, turn := freePort(t), freePort(t), freePort(t)
	relayBlock.Lock()
	relayBlock.n++
	base := 46000 + (os.Getpid()%40)*100 + relayBlock.n*10
	relayBlock.Unlock()
	env := append(os.Environ(),
		fmt.Sprintf("RDV_LISTEN=127.0.0.1:%d", listen),
		fmt.Sprintf("RDV_ADMIN_LISTEN=127.0.0.1:%d", admin),
		"RDV_PUBLIC_IP=127.0.0.1",
		"RDV_ICE_HOST=127.0.0.1",
		fmt.Sprintf("RDV_TURN_PORT=%d", turn),
		"RDV_TURN_LISTEN_IP=127.0.0.1",
		fmt.Sprintf("RDV_RELAY_PORTS=%d-%d", base, base+9),
		"RDV_RELAY_ALLOW_PRIVATE=true",
		"RDV_RELAY_MAX_KBPS_SESSION=200000",
		"RDV_DATA_DIR="+dir,
		"RDV_OPERATOR_KEYS=helpdesk:"+xKey,
		"RDV_LOG_LEVEL=warn",
	)
	r := &xRDV{admin: fmt.Sprintf("http://127.0.0.1:%d", admin), url: fmt.Sprintf("ws://127.0.0.1:%d", listen)}
	if withTLS {
		cert, key, pool := selfSignedPEM(t)
		r.ca = filepath.Join(dir, "ca.pem")
		keyFile := filepath.Join(dir, "key.pem")
		if err := os.WriteFile(r.ca, cert, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, key, 0o600); err != nil {
			t.Fatal(err)
		}
		env = append(env, "RDV_TLS=files", "RDV_TLS_CERT="+r.ca, "RDV_TLS_KEY="+keyFile, "RDV_TURNS_HOST=127.0.0.1")
		r.url = fmt.Sprintf("wss://127.0.0.1:%d", listen)
		r.tlsConf = &tls.Config{RootCAs: pool}
	} else {
		env = append(env, "RDV_TLS=off")
	}
	cmd := exec.Command(bin)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(r.admin + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("rendezvous server did not start")
	return nil
}

func selfSignedPEM(t *testing.T) (certPEM, keyPEM []byte, pool *x509.CertPool) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(c)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), pool
}

// xPeer is a running nt6 linkpeer.
type xPeer struct {
	cmd   *exec.Cmd
	lines chan string
}

func startPeer(t *testing.T, bin string, args ...string) *xPeer {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &xPeer{cmd: cmd, lines: make(chan string, 16)}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return p
}

// expect returns the fields after the next line starting with word.
func (p *xPeer) expect(t *testing.T, word string, d time.Duration) []string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				t.Fatalf("nt6 peer exited before %s", word)
			}
			if strings.HasPrefix(l, "ERROR ") {
				t.Fatalf("nt6 peer: %s", l)
			}
			if f := strings.Fields(l); len(f) > 0 && f[0] == word {
				return f[1:]
			}
		case <-deadline:
			t.Fatalf("nt6 peer: no %s line", word)
		}
	}
}

func (p *xPeer) wait(t *testing.T) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nt6 peer exit: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("nt6 peer did not exit")
	}
}

type xPath struct {
	name     string
	tls      bool
	relay    bool
	tcp      bool
	tlsOnly  bool
	want     sl.Path
	wantKept int // relay allocations left once the unused ones are released
}

func (c xPath) ice() sl.ICEOptions {
	return sl.ICEOptions{
		IncludeLoopback: true, IPFilter: func(ip net.IP) bool { return ip.IsLoopback() },
		ForceRelay: c.relay, TURNTCPOnly: c.tcp, TURNTLSOnly: c.tlsOnly,
	}
}

func (c xPath) flags() []string {
	var f []string
	if c.relay {
		f = append(f, "-relay")
	}
	if c.tcp {
		f = append(f, "-tcp")
	}
	if c.tlsOnly {
		f = append(f, "-tls")
	}
	return f
}

// waitAllocations waits for the rendezvous server to hold want relay
// allocations at most (the clients release the unused ones 3 s after the
// selected pair settles).
func waitAllocations(t *testing.T, r *xRDV, want int) (peak, last int) {
	t.Helper()
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		last = r.metric(t, "rdv_relay_active")
		if last > peak {
			peak = last
		}
		if last >= 0 && last <= want {
			return peak, last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("relay allocations: %d left (peak %d), want <= %d", last, peak, want)
	return peak, last
}

func TestCrossLine(t *testing.T) {
	if os.Getenv("SUPPORTLINK_INTEROP") != "1" {
		t.Skip("set SUPPORTLINK_INTEROP=1 (builds rdv and the Go 1.20 nt6 helper)")
	}
	bins := buildBins(t)
	paths := []xPath{
		{name: "direct", want: sl.PathDirect, wantKept: 0},
		{name: "relay-udp", relay: true, want: sl.PathRelayUDP, wantKept: 2},
		{name: "relay-tcp", relay: true, tcp: true, want: sl.PathRelayTCP, wantKept: 2},
		{name: "relay-tls", tls: true, relay: true, tlsOnly: true, want: sl.PathRelayTLS, wantKept: 2},
	}
	var report []string
	for _, pc := range paths {
		pc := pc
		t.Run("nt6-host/modern-operator/"+pc.name, func(t *testing.T) {
			r := startRDV(t, bins.rdv, pc.tls)
			args := append([]string{"-role", "host", "-server", r.url, "-n", strconv.Itoa(xFrames)}, pc.flags()...)
			if r.ca != "" {
				args = append(args, "-ca", r.ca)
			}
			p := startPeer(t, bins.peer, args...)
			w := p.expect(t, "WAITING", 30*time.Second)
			t0 := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			s, err := sl.Connect(ctx, sl.OperatorConfig{
				Server: r.url, ClientVersion: "1.0.0", Key: xKey, Name: "modern operator", ICE: pc.ice(),
				SignalOptions: sl.SignalOptions{TLSConfig: r.tlsConf, Proxy: sl.ProxyDirect},
			}, w[0], w[1])
			cancel()
			if err != nil {
				t.Fatalf("modern operator connect: %v", err)
			}
			defer s.Close()
			setup := time.Since(t0)
			hs := p.expect(t, "SESSION", 30*time.Second)
			if s.Path() != pc.want || hs[0] != string(pc.want) {
				t.Fatalf("path: modern operator %s (%s), nt6 host %v; want %s", s.Path(), s.PathDetail(), hs, pc.want)
			}
			if strings.ReplaceAll(s.SAS(), " ", "_") != hs[1] {
				t.Fatalf("SAS: modern %q, nt6 %q", s.SAS(), hs[1])
			}
			rep, err := interop.Run(s, false, xFrames)
			if err != nil {
				t.Fatalf("modern operator exchange: %v", err)
			}
			hr := strings.Join(p.expect(t, "REPORT", 60*time.Second), " ")
			peak, last := waitAllocations(t, r, pc.wantKept)
			s.Close()
			p.wait(t)
			line := fmt.Sprintf("nt6 host <-> modern operator, %-9s setup %v, pair %s; nt6 host: %s; modern operator received %d frames, %.1f MiB in %v; relay allocations peak %d, kept %d",
				pc.want, setup.Round(time.Millisecond), s.PathDetail(), hr, rep.Frames, float64(rep.Bytes)/(1<<20), rep.Elapsed.Round(time.Millisecond), peak, last)
			t.Log(line)
			report = append(report, line)
		})
		t.Run("modern-host/nt6-operator/"+pc.name, func(t *testing.T) {
			r := startRDV(t, bins.rdv, pc.tls)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			h, err := sl.Register(ctx, sl.HostConfig{
				Server: r.url, ClientVersion: "1.0.0", ICE: pc.ice(),
				SignalOptions: sl.SignalOptions{TLSConfig: r.tlsConf, Proxy: sl.ProxyDirect},
				Approve:       func(context.Context, sl.ApprovalRequest) bool { return true },
			})
			cancel()
			if err != nil {
				t.Fatalf("modern host register: %v", err)
			}
			defer h.Close()
			args := append([]string{"-role", "operator", "-server", r.url, "-key", xKey, "-id", h.ID(), "-code", h.Code(),
				"-n", strconv.Itoa(xFrames), "-hold", "8s"}, pc.flags()...)
			if r.ca != "" {
				args = append(args, "-ca", r.ca)
			}
			t0 := time.Now()
			p := startPeer(t, bins.peer, args...)
			ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
			s, err := h.WaitForOperator(ctx)
			cancel()
			if err != nil {
				t.Fatalf("modern host: %v", err)
			}
			defer s.Close()
			setup := time.Since(t0)
			ops := p.expect(t, "SESSION", 30*time.Second)
			if s.Path() != pc.want || ops[0] != string(pc.want) {
				t.Fatalf("path: modern host %s (%s), nt6 operator %v; want %s", s.Path(), s.PathDetail(), ops, pc.want)
			}
			if strings.ReplaceAll(s.SAS(), " ", "_") != ops[1] {
				t.Fatalf("SAS: modern %q, nt6 %q", s.SAS(), ops[1])
			}
			rep, err := interop.Run(s, true, xFrames)
			if err != nil {
				t.Fatalf("modern host exchange: %v", err)
			}
			orep := strings.Join(p.expect(t, "REPORT", 60*time.Second), " ")
			peak, last := waitAllocations(t, r, pc.wantKept)
			select {
			case <-s.Done():
			case <-time.After(20 * time.Second):
				t.Fatal("the close by the nt6 operator did not reach the modern host")
			}
			p.wait(t)
			line := fmt.Sprintf("modern host <-> nt6 operator, %-9s setup %v (incl. process start), pair %s; modern host: %s; nt6 operator: %s; relay allocations peak %d, kept %d",
				pc.want, setup.Round(time.Millisecond), s.PathDetail(), rep, orep, peak, last)
			t.Log(line)
			report = append(report, line)
		})
	}
	for _, l := range report {
		t.Log(l)
	}
}
