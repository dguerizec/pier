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

func TestContainerAttachedToNetwork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script docker stub is POSIX-only")
	}

	stub := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = inspect ]; then
  if [ "$PIER_TEST_ATTACHED" = true ]; then
    echo true
  fi
fi
`
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := newDocker()
	t.Setenv("PIER_TEST_ATTACHED", "true")
	if !d.containerAttachedToNetwork(TraefikContainer, NetworkName) {
		t.Fatal("containerAttachedToNetwork() = false, want true")
	}
	t.Setenv("PIER_TEST_ATTACHED", "false")
	if d.containerAttachedToNetwork(TraefikContainer, NetworkName) {
		t.Fatal("containerAttachedToNetwork() = true, want false")
	}
}

func TestRemoveContainersByLabel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script docker stub is POSIX-only")
	}

	stub := t.TempDir()
	log := filepath.Join(stub, "calls")
	script := `#!/bin/sh
echo "$*" >> "$PIER_TEST_DOCKER_LOG"
if [ "$1" = ps ]; then
  printf 'pier-share-a\npier-share-b\n'
fi
`
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PIER_TEST_DOCKER_LOG", log)

	removed, err := newDocker().removeContainersByLabel("dev.pier.component=share")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "pier-share-a,pier-share-b" {
		t.Fatalf("removed = %v", removed)
	}
	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"ps -a --filter label=dev.pier.component=share --format {{.Names}}",
		"rm -f pier-share-a",
		"rm -f pier-share-b",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("calls missing %q:\n%s", want, got)
		}
	}
}
