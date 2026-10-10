package main

// Configuration of the thin client. The rendezvous server is resolved in
// this order:
//
//  1. the -server flag;
//  2. OblireachSupport.json next to the executable (a customised product
//     ships one; optional);
//  3. the compiled-in DefaultServer.
//
// Nothing is ever written: the JSON file is read only.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultServer is the public rendezvous instance, used when neither the
// -server flag nor a product file names one.
const DefaultServer = "https://rdv.reach.obli.tools"

// DefaultServerName is what the person sees for DefaultServer.
const DefaultServerName = "Oblireach"

// ConfigFileName is the optional product file read next to the executable.
const ConfigFileName = "OblireachSupport.json"

// maxConfigSize bounds the product file (it holds a few short strings).
const maxConfigSize = 64 << 10

// fileConfig is the shape of OblireachSupport.json.
type fileConfig struct {
	// Server is the rendezvous server ("support.example.org",
	// "https://support.example.org" or "wss://support.example.org/v1/ws").
	Server string `json:"server"`
	// ServerName is the label shown to the person ("Example IT support").
	// Default: the server host name.
	ServerName string `json:"serverName"`
}

// Config is the resolved configuration.
type Config struct {
	Server     string // as given (normalised by supportlink when dialling)
	ServerName string // what the person sees
	Source     string // "flag", "file" or "default" (diagnostics)
}

// resolveConfig applies the precedence rules. flagServer is the -server
// value ("" when absent); exeDir is the directory of the executable ("" to
// skip the product file).
func resolveConfig(flagServer, exeDir string) (Config, error) {
	var fc fileConfig
	haveFile := false
	if exeDir != "" {
		var err error
		fc, haveFile, err = readFileConfig(filepath.Join(exeDir, ConfigFileName))
		if err != nil {
			return Config{}, err
		}
	}
	c := Config{Server: DefaultServer, Source: "default"}
	switch {
	case strings.TrimSpace(flagServer) != "":
		c.Server, c.Source = strings.TrimSpace(flagServer), "flag"
	case haveFile && strings.TrimSpace(fc.Server) != "":
		c.Server, c.Source = strings.TrimSpace(fc.Server), "file"
	}
	host, err := serverHost(c.Server)
	if err != nil {
		return Config{}, err
	}
	c.ServerName = host
	if c.Source == "default" {
		c.ServerName = DefaultServerName
	}
	// The product's label only describes the product's own server.
	if haveFile && c.Source != "flag" {
		if n := cleanLabel(fc.ServerName); n != "" {
			c.ServerName = n
		}
	}
	return c, nil
}

// readFileConfig reads the product file. A missing file is not an error; a
// present but unreadable or malformed one is (a customised product must not
// silently fall back to another server).
func readFileConfig(path string) (fileConfig, bool, error) {
	var fc fileConfig
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fc, false, nil
	}
	if err != nil {
		return fc, false, fmt.Errorf("%s: %w", ConfigFileName, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return fc, false, fmt.Errorf("%s: %w", ConfigFileName, err)
	}
	if len(b) > maxConfigSize {
		return fc, false, fmt.Errorf("%s: file too large", ConfigFileName)
	}
	b = trimBOM(b)
	if err := json.Unmarshal(b, &fc); err != nil {
		return fc, false, fmt.Errorf("%s: %w", ConfigFileName, err)
	}
	return fc, true, nil
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// serverHost validates a server setting and returns its host name.
func serverHost(server string) (string, error) {
	s := strings.TrimSpace(server)
	if s == "" {
		return "", errors.New("no rendezvous server configured")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("bad server URL %q: %w", server, err)
	}
	switch u.Scheme {
	case "https", "wss", "http", "ws":
	default:
		return "", fmt.Errorf("bad server URL %q: unsupported scheme", server)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("bad server URL %q: no host", server)
	}
	if u.User != nil {
		return "", fmt.Errorf("bad server URL %q: credentials are not allowed in the URL", server)
	}
	return u.Hostname(), nil
}

// cleanLabel bounds a display label read from the product file.
func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}
