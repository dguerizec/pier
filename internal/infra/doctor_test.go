package infra

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckManagedTraefikReportsMissingNetwork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script docker stub is POSIX-only")
	}

	stub := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = inspect ] && [ "$3" = '{{.State.Running}}' ]; then
  echo true
fi
`
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	check := checkManagedTraefik()
	if check.Status != StatusFail {
		t.Fatalf("status = %v, want failure: %#v", check.Status, check)
	}
	if !strings.Contains(check.Detail, "not attached to docker network "+NetworkName) {
		t.Errorf("detail = %q", check.Detail)
	}
	if !strings.Contains(check.FixHint, "pier doctor --fix") {
		t.Errorf("fix hint = %q", check.FixHint)
	}
}

func TestReconnectManagedTraefik(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script docker stub is POSIX-only")
	}

	stub := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	script := `#!/bin/sh
if [ "$1" = inspect ] && [ "$3" = '{{.State.Running}}' ]; then
  echo true
  exit 0
fi
if [ "$1" = network ] && [ "$2" = connect ]; then
  echo "$@" > "$PIER_TEST_DOCKER_LOG"
fi
`
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PIER_TEST_DOCKER_LOG", logPath)

	reconnected, err := reconnectManagedTraefik(newDocker())
	if err != nil {
		t.Fatalf("reconnectManagedTraefik(): %v", err)
	}
	if !reconnected {
		t.Fatal("reconnectManagedTraefik() = false, want true")
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker invocation: %v", err)
	}
	want := "network connect " + NetworkName + " " + TraefikContainer
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("docker invocation = %q, want %q", strings.TrimSpace(string(got)), want)
	}
}
