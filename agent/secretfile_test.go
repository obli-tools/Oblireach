package main

import "testing"

func TestStripServiceSecretArgs(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{`"C:\Program Files\OblireachAgent\oblireach-agent.exe" --url "https://o.example" --key "abc"`,
			`"C:\Program Files\OblireachAgent\oblireach-agent.exe"`, true},
		{`"C:\Program Files\OblireachAgent\oblireach-agent.exe" --url "" --key ""`,
			`"C:\Program Files\OblireachAgent\oblireach-agent.exe"`, true},
		{`C:\OblireachAgent\oblireach-agent.exe -url=https://o -key=abc`,
			`"C:\OblireachAgent\oblireach-agent.exe"`, true},
		{`"C:\Program Files\OblireachAgent\oblireach-agent.exe" --key=abc`,
			`"C:\Program Files\OblireachAgent\oblireach-agent.exe"`, true},
		// Nothing to strip.
		{`"C:\Program Files\OblireachAgent\oblireach-agent.exe"`, "", false},
		{`"C:\Program Files\OblireachAgent\oblireach-agent.exe" --url "https://o"`, "", false},
		{`C:\OblireachAgent\oblireach-agent.exe`, "", false},
		{`"C:\broken --key x`, "", false},
		{``, "", false},
	}
	for _, c := range cases {
		got, ok := stripServiceSecretArgs(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("stripServiceSecretArgs(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
