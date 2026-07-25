package share

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDockerRuntimeStartUsesSelectiveGatewayEntrypoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script docker stub is POSIX-only")
	}
	stub := t.TempDir()
	logPath := filepath.Join(stub, "calls")
	readyPath := filepath.Join(stub, "gateway", ".ready")
	if err := os.MkdirAll(filepath.Dir(readyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$*" >> "$PIER_TEST_DOCKER_LOG"
touch "$PIER_TEST_SHARE_READY"
echo fake-container-id
`
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PIER_TEST_DOCKER_LOG", logPath)
	t.Setenv("PIER_TEST_SHARE_READY", readyPath)

	err := NewDockerRuntime().Start(gatewaySpec{
		Name:       "pier-share-test",
		Network:    "pier",
		BindIP:     "192.168.1.42",
		Restart:    true,
		StaticPath: "/config/share/traefik.yml",
		DataPath:   "/config/share/gateways/test",
		ReadyPath:  readyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"--network pier",
		"--restart unless-stopped",
		"--label dev.pier.component=share",
		"-p 192.168.1.42:80:80/tcp",
		"--entrypoint /bin/sh",
		"sleep 0.25",
		`kill -0 "$$"`,
		"exec traefik --configFile=/etc/traefik/traefik.yml",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("docker args missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "exec /traefik") {
		t.Fatalf("docker args use an image-specific binary path:\n%s", got)
	}
}
