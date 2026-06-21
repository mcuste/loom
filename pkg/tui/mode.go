package tui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// realIsTerminal reports whether w is connected to a terminal by inspecting its
// file descriptor via golang.org/x/term. A writer that is not an *os.File (a
// buffer, a pipe) is never a terminal.
func realIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// richConfig holds the probes Rich consults. The defaults read the real
// terminal and the process environment; an Option swaps in a deterministic fake
// so Rich keeps no shared mutable package state and stays safe to call (and
// test) concurrently.
type richConfig struct {
	isTerminal func(io.Writer) bool
	lookupEnv  func(string) (string, bool)
}

// Option overrides one of Rich's probes. Callers that want the production
// behaviour pass none.
type Option func(*richConfig)

// WithIsTerminal replaces the terminal probe.
func WithIsTerminal(fn func(io.Writer) bool) Option {
	return func(c *richConfig) { c.isTerminal = fn }
}

// WithLookupEnv replaces the environment lookup (used for NO_COLOR and TERM).
func WithLookupEnv(fn func(string) (string, bool)) Option {
	return func(c *richConfig) { c.lookupEnv = fn }
}

// Rich reports whether w should receive the rich TUI rendering. It is true only
// when w is a terminal, the NO_COLOR environment variable is unset, and TERM is
// not "dumb"; otherwise false. NO_COLOR disables even when set empty, matching
// the no-color.org convention. The probes default to the real terminal and
// process environment; pass Options to inject fakes.
func Rich(w io.Writer, opts ...Option) bool {
	cfg := richConfig{isTerminal: realIsTerminal, lookupEnv: os.LookupEnv}
	for _, opt := range opts {
		opt(&cfg)
	}
	if !cfg.isTerminal(w) {
		return false
	}
	if _, ok := cfg.lookupEnv("NO_COLOR"); ok {
		return false
	}
	if v, _ := cfg.lookupEnv("TERM"); v == "dumb" {
		return false
	}
	return true
}
