package cli

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dguerizec/pier/internal/state"
)

func TestWaitForHealthyRetriesUntilSuccess(t *testing.T) {
	attempts := 0
	if !waitForHealthy(func() bool {
		attempts++
		return attempts == 3
	}, time.Second, 0) {
		t.Fatal("waitForHealthy() = false, want true")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestWaitForHealthyStopsAtDeadline(t *testing.T) {
	attempts := 0
	if waitForHealthy(func() bool {
		attempts++
		return false
	}, 0, 0) {
		t.Fatal("waitForHealthy() = true, want false")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

func TestWorkloadRouteFailureDialsTraefikAddress(t *testing.T) {
	worktree := t.TempDir()
	manifest := `[project]
name = "routecheck"

[stack]
kind = "compose"
file = "compose.yml"
service = "web"

[[expose]]
service = "web"
port = 3000
`
	if err := os.WriteFile(filepath.Join(worktree, ".pier.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err == nil && req.Host != "main.routecheck.invalid" {
			err = fmt.Errorf("Host = %q", req.Host)
		}
		if err == nil {
			_, err = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		}
		served <- err
	}()

	w := &state.Workload{Project: "routecheck", Slug: "main", WorktreePath: worktree}
	if detail := workloadRouteFailure(w, "invalid", listener.Addr().String()); detail != "" {
		t.Fatalf("workloadRouteFailure() = %q, want healthy route", detail)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
