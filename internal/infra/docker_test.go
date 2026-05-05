package infra

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDockerRun_ExitErrorPreservesChain locks the wrap contract on
// (*docker).run: when docker exits non-zero, the returned error must
// (a) include the captured stderr in its message and (b) wrap the
// original *exec.ExitError so errors.As can recover it. The previous
// formatting used %s for ExitError, flattening the chain — fine for
// display but it broke errors.As-based introspection.
func TestDockerRun_ExitErrorPreservesChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script docker stub is POSIX-only")
	}

	stub := t.TempDir()
	const stderrMarker = "FAKE_DOCKER_STDERR_MSG"
	script := "#!/bin/sh\n" +
		"echo " + stderrMarker + " >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := newDocker()
	_, err := d.run("anything")
	if err == nil {
		t.Fatal("expected error from non-zero exit, got nil")
	}

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Errorf("errors.As(*exec.ExitError) failed — wrap chain broken: %v", err)
	}
	if !strings.Contains(err.Error(), stderrMarker) {
		t.Errorf("error message missing stderr text %q: %v", stderrMarker, err)
	}
	if !strings.Contains(err.Error(), "docker anything") {
		t.Errorf("error message missing argv prefix: %v", err)
	}
}

// TestDockerPull_ExitErrorPreservesChain mirrors the run() contract for
// (*docker).pull. Combined output replaces stderr-only capture, but the
// wrap rule stays: surface the user-facing text and keep the chain.
func TestDockerPull_ExitErrorPreservesChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script docker stub is POSIX-only")
	}

	stub := t.TempDir()
	const outputMarker = "FAKE_DOCKER_PULL_OUTPUT"
	script := "#!/bin/sh\n" +
		"echo " + outputMarker + "\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := newDocker()
	err := d.pull("ghcr.io/example/img:latest")
	if err == nil {
		t.Fatal("expected error from non-zero exit, got nil")
	}

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Errorf("errors.As(*exec.ExitError) failed — wrap chain broken: %v", err)
	}
	if !strings.Contains(err.Error(), outputMarker) {
		t.Errorf("error message missing combined-output text %q: %v", outputMarker, err)
	}
	if !strings.Contains(err.Error(), "docker pull") {
		t.Errorf("error message missing 'docker pull' prefix: %v", err)
	}
}
