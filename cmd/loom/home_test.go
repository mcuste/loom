package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoomHome_HonorsEnvAndCreatesDir pins that loomHome returns $LOOM_HOME
// verbatim when it is set and creates the directory if it does not yet exist.
func TestLoomHome_HonorsEnvAndCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom-home")
	t.Setenv("LOOM_HOME", dir)

	got, err := loomHome()
	if err != nil {
		t.Fatalf("loomHome: %v", err)
	}
	if got != dir {
		t.Errorf("loomHome = %q, want %q", got, dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("loom home dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("loom home %q is not a directory", dir)
	}
}

// TestLoomHome_ResolvesRelativeEnvToAbs pins that a relative LOOM_HOME is
// resolved to an absolute path (via filepath.Abs) before use, so the two
// loomHome calls that straddle a resume-time chdir agree on one on-disk
// location instead of silently splitting the store.
func TestLoomHome_ResolvesRelativeEnvToAbs(t *testing.T) {
	chdirTo(t, t.TempDir())
	t.Setenv("LOOM_HOME", "rel-home")

	got, err := loomHome()
	if err != nil {
		t.Fatalf("loomHome: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("loomHome = %q, want an absolute path", got)
	}
	// filepath.Abs resolves against os.Getwd(); derive want the same way so the
	// comparison holds even where the temp dir is a symlink (e.g. macOS /var).
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	want := filepath.Join(cwd, "rel-home")
	if got != want {
		t.Errorf("loomHome = %q, want %q", got, want)
	}
	if info, err := os.Stat(got); err != nil {
		t.Fatalf("relative loom home dir not created: %v", err)
	} else if !info.IsDir() {
		t.Errorf("loom home %q is not a directory", got)
	}
}

// TestLoomHome_FallsBackToUserHomeDir pins that with LOOM_HOME unset, loomHome
// resolves $HOME/.loom and creates it.
func TestLoomHome_FallsBackToUserHomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", "")
	t.Setenv("HOME", home)

	got, err := loomHome()
	if err != nil {
		t.Fatalf("loomHome: %v", err)
	}
	want := filepath.Join(home, ".loom")
	if got != want {
		t.Errorf("loomHome = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("fallback home dir not created: %v", err)
	}
}
