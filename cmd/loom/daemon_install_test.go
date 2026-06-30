package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSpec builds a unitSpec writing into dir with two enable steps, used by the
// installUnit characterization tests below.
func testSpec(dir string) unitSpec {
	return unitSpec{
		dir:      dir,
		filename: "loom-daemon.service",
		content:  "UNIT CONTENT\n",
		enableSteps: [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", "--now", "loom-daemon"},
		},
		noun:       "systemd user unit",
		successMsg: "enabled systemd unit; the daemon is now running and will start at login\n",
	}
}

// TestInstallUnit_Manual pins the manual flow: installUnit writes the unit file
// (content, 0644) and prints the enable steps for the user, without invoking the
// runner.
func TestInstallUnit_Manual(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "user")
	spec := testSpec(dir)
	var out bytes.Buffer
	ran := false
	run := func(io.Writer, []string) error { ran = true; return nil }

	if err := installUnit(&out, spec, true, run); err != nil {
		t.Fatalf("installUnit manual: %v", err)
	}

	path := filepath.Join(dir, "loom-daemon.service")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written unit: %v", err)
	}
	if string(got) != spec.content {
		t.Errorf("unit content = %q, want %q", got, spec.content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat written unit: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("unit mode = %v, want 0644", info.Mode().Perm())
	}
	if ran {
		t.Error("manual install ran an enable step; want none")
	}
	want := "wrote systemd user unit " + path + "\n\nenable it with:\n" +
		"  systemctl --user daemon-reload\n" +
		"  systemctl --user enable --now loom-daemon\n"
	if out.String() != want {
		t.Errorf("manual output = %q, want %q", out.String(), want)
	}
}

// TestInstallUnit_AutoEnable pins the auto flow: installUnit writes the unit,
// runs each enable step in order, and prints the success line.
func TestInstallUnit_AutoEnable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "user")
	spec := testSpec(dir)
	var out bytes.Buffer
	var gotSteps [][]string
	run := func(_ io.Writer, args []string) error {
		gotSteps = append(gotSteps, args)
		return nil
	}

	if err := installUnit(&out, spec, false, run); err != nil {
		t.Fatalf("installUnit auto: %v", err)
	}

	path := filepath.Join(dir, "loom-daemon.service")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	if len(gotSteps) != 2 || gotSteps[0][1] != "--user" || gotSteps[1][3] != "--now" {
		t.Errorf("ran steps = %v, want the two enable steps in order", gotSteps)
	}
	if !strings.HasPrefix(out.String(), "wrote systemd user unit "+path+"\n") {
		t.Errorf("auto output = %q, want it to start with the wrote line", out.String())
	}
	if !strings.HasSuffix(out.String(), spec.successMsg) {
		t.Errorf("auto output = %q, want it to end with the success line", out.String())
	}
}

// TestInstallUnit_EnableStepFails pins the failure flow: a failing enable step
// is wrapped with the joined argv and the --manual hint, and the underlying
// error stays reachable via errors.Is.
func TestInstallUnit_EnableStepFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "user")
	spec := testSpec(dir)
	var out bytes.Buffer
	boom := errors.New("boom")
	run := func(io.Writer, []string) error { return boom }

	err := installUnit(&out, spec, false, run)
	if err == nil {
		t.Fatal("installUnit returned nil error when an enable step failed; want error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %q does not wrap the step failure", err.Error())
	}
	if !strings.Contains(err.Error(), "systemctl --user daemon-reload") {
		t.Errorf("error %q does not name the failed step", err.Error())
	}
	if !strings.Contains(err.Error(), "re-run with --manual") {
		t.Errorf("error %q does not carry the --manual hint", err.Error())
	}
}
