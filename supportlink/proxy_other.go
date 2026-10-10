//go:build !windows

package supportlink

import "net/url"

// systemProxy: no system-wide proxy setting outside Windows beyond the
// environment variables, already handled by resolveProxy.
func systemProxy(*url.URL) (*url.URL, error) { return nil, nil }
