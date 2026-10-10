//go:build windows

package supportlink

import (
	"net/url"
	"testing"
)

func TestWinHTTPProxyList(t *testing.T) {
	for _, c := range []struct{ list, scheme, want string }{
		{"proxy:8080", "https", "http://proxy:8080"},
		{"http=hp:80;https=sp:443", "https", "http://sp:443"},
		{"http=hp:80;https=sp:443", "http", "http://hp:80"},
		{"http=hp:80", "https", "http://hp:80"},
		{"socks=s:1080", "https", ""},
		{"p1:3128;p2:3128", "https", "http://p1:3128"},
	} {
		got := ""
		if u := pickProxy(c.list, c.scheme); u != nil {
			got = u.String()
		}
		if got != c.want {
			t.Fatalf("pickProxy(%q, %s) = %q, want %q", c.list, c.scheme, got, c.want)
		}
	}
	for _, c := range []struct {
		host, list string
		want       bool
	}{
		{"intranet", "<local>", true},
		{"rdv.example.com", "<local>", false},
		{"rdv.corp.example", "*.corp.example;10.*", true},
		{"10.1.2.3", "*.corp.example;10.*", true},
		{"rdv.example.com", "*.corp.example", false},
	} {
		if got := bypassed(c.host, c.list); got != c.want {
			t.Fatalf("bypassed(%q, %q) = %v", c.host, c.list, got)
		}
	}
	// The system lookup itself must not fail (its answer depends on the
	// machine).
	if _, err := systemProxy(&url.URL{Scheme: "https", Host: "rdv.example.com:443"}); err != nil {
		t.Fatal(err)
	}
}
