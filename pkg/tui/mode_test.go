package tui

import (
	"bytes"
	"io"
	"testing"
)

// TestRich_GatesOnTTYNoColorAndTerm pins the three-way gate: Rich is true only
// when w is a terminal, NO_COLOR is unset, and TERM is not "dumb". Both the
// terminal probe and the environment lookup are injected per-call, so the table
// holds no shared mutable state and every sub-test runs in parallel.
func TestRich_GatesOnTTYNoColorAndTerm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tty  bool
		env  map[string]string // a key's absence means it is unset
		want bool
	}{
		{name: "tty, NO_COLOR unset, TERM normal", tty: true, env: map[string]string{"TERM": "xterm-256color"}, want: true},
		{name: "not a tty", tty: false, env: map[string]string{"TERM": "xterm-256color"}, want: false},
		{name: "tty but NO_COLOR set", tty: true, env: map[string]string{"NO_COLOR": "1", "TERM": "xterm-256color"}, want: false},
		{name: "tty but NO_COLOR set empty still disables", tty: true, env: map[string]string{"NO_COLOR": "", "TERM": "xterm-256color"}, want: false},
		{name: "tty but TERM is dumb", tty: true, env: map[string]string{"TERM": "dumb"}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookup := func(key string) (string, bool) {
				v, ok := tc.env[key]
				return v, ok
			}
			got := Rich(&bytes.Buffer{},
				WithIsTerminal(func(io.Writer) bool { return tc.tty }),
				WithLookupEnv(lookup))
			if got != tc.want {
				t.Errorf("Rich() = %v, want %v", got, tc.want)
			}
		})
	}
}
