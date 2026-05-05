package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPsCmd_DockerOutputCapturedViaCobraWriter asserts that `pier ps`
// pipes the docker subprocess stdout/stderr through cobra's writers, so
// that test (and any future programmatic) capture via cmd.SetOut works.
// The previous wiring assigned os.Stdout/os.Stderr directly to the
// exec.Cmd, making the output invisible to anything that wrapped cobra.
//
// The test uses a fake `docker` shim on PATH so it never depends on
// docker being installed; --project bypasses resolveDaily so the test
// also doesn't need a manifest or worktree.
func TestPsCmd_DockerOutputCapturedViaCobraWriter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script docker stub is POSIX-only")
	}

	stubDir := t.TempDir()
	dockerStub := filepath.Join(stubDir, "docker")
	const stdoutMarker = "FAKE_DOCKER_STDOUT"
	const stderrMarker = "FAKE_DOCKER_STDERR"
	script := "#!/bin/sh\n" +
		"echo " + stdoutMarker + "\n" +
		"echo " + stderrMarker + " >&2\n"
	if err := os.WriteFile(dockerStub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := newPsCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	// --project skips resolveDaily so we don't need a worktree or manifest
	// for this wiring test. The full resolveDaily path is exercised by
	// TestDailyForWorktree_WritersPropagate.
	cmd.SetArgs([]string{"--project", "demo"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstdout: %q\nstderr: %q", err, out.String(), errOut.String())
	}

	if !strings.Contains(out.String(), stdoutMarker) {
		t.Errorf("docker stdout was not captured by cobra writer: got %q", out.String())
	}
	if !strings.Contains(errOut.String(), stderrMarker) {
		t.Errorf("docker stderr was not captured by cobra error writer: got %q", errOut.String())
	}
}
