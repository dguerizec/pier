package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/worktree"
)

// TestDailyForWorktree_WritersPropagate locks the contract that the (out,
// errW) parameters reach d.Ctx.Out / d.Ctx.Err — i.e. compose adapter
// output goes where the caller asked, not to a hardcoded os.Stdout. The
// tier-1 capture bug was that resolveDaily passed os.Stdout regardless of
// what cobra had been told via SetOut; this test exists so that
// regression can't slip back in without breaking.
func TestDailyForWorktree_WritersPropagate(t *testing.T) {
	root := t.TempDir()

	// Point pier's config dir at the temp tree so DefaultPaths() and
	// LoadConfig() don't touch the developer's actual ~/.config/pier.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

	paths, err := infra.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	cfg := &infra.Config{
		Mode:           infra.ModeLocal,
		TLD:            "test",
		BindIP:         "127.0.0.1",
		AnswerIP:       "127.0.0.1",
		TraefikNetwork: "pier",
	}
	if err := cfg.Save(paths); err != nil {
		t.Fatalf("save config: %v", err)
	}

	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestBody := `[project]
name = "demo"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"

[[expose]]
service = "web"
port = 3000
`
	if err := os.WriteFile(filepath.Join(wt, ".pier.toml"), []byte(manifestBody), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	info := &worktree.Info{
		Toplevel:    wt,
		Branch:      "main",
		PrimaryPath: wt,
		IsPrimary:   true,
	}

	var out, errOut bytes.Buffer
	d, err := dailyForWorktree(info, "main", &out, &errOut)
	if err != nil {
		t.Fatalf("dailyForWorktree: %v", err)
	}
	defer d.State.Close()

	// Write through the adapter context's writers and confirm the bytes
	// land in the caller's buffers — that's the exact path the compose
	// adapter uses for `docker compose up/down/logs` output.
	fmt.Fprint(d.Ctx.Out, "OUT-MARKER")
	fmt.Fprint(d.Ctx.Err, "ERR-MARKER")

	if got := out.String(); got != "OUT-MARKER" {
		t.Errorf("d.Ctx.Out did not forward to caller: got %q", got)
	}
	if got := errOut.String(); got != "ERR-MARKER" {
		t.Errorf("d.Ctx.Err did not forward to caller: got %q", got)
	}
}

func TestDailyForWorktree_ResolveValuesRefreshAndCache(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

	paths, err := infra.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cfg := &infra.Config{
		Mode:           infra.ModeLocal,
		TLD:            "test",
		BindIP:         "127.0.0.1",
		AnswerIP:       "127.0.0.1",
		TraefikNetwork: "pier",
	}
	if err := cfg.Save(paths); err != nil {
		t.Fatal(err)
	}

	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestBody := `[project]
name = "pickatube"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"

[[expose]]
service = "backend"
port = 8000
preserve_ports = [{value.oauth_callback_port}]

[env.backend]
GOOGLE_OAUTH_REDIRECT_URI = "http://127.0.0.1:{value.oauth_callback_port}/callback"

[hooks]
resolve_values = "./resolve-values.sh"
`
	if err := os.WriteFile(filepath.Join(wt, ".pier.toml"), []byte(manifestBody), 0o644); err != nil {
		t.Fatal(err)
	}
	writeResolver := func(port int) {
		t.Helper()
		body := fmt.Sprintf("#!/bin/sh\nprintf '{\"oauth_callback_port\":%d}\\n'\n", port)
		if err := os.WriteFile(filepath.Join(wt, "resolve-values.sh"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeResolver(49163)

	info := &worktree.Info{
		Toplevel:    wt,
		Branch:      "feat/oauth",
		PrimaryPath: wt,
		IsPrimary:   true,
	}
	var out, errOut bytes.Buffer
	fresh, err := dailyForWorktreeFresh(info, "oauth", &out, &errOut)
	if err != nil {
		t.Fatalf("fresh daily: %v\nstderr: %s", err, errOut.String())
	}
	if got := fresh.Manifest.Expose[0].PreservePorts[0]; got != 49163 {
		t.Errorf("fresh preserve port = %d, want 49163", got)
	}
	if got := fresh.Ctx.ComposeEnv["PIER_VALUE_OAUTH_CALLBACK_PORT"]; got != "49163" {
		t.Errorf("fresh compose env = %q", got)
	}
	fresh.State.Close()

	writeResolver(49164)
	cached, err := dailyForWorktree(info, "oauth", &out, &errOut)
	if err != nil {
		t.Fatalf("cached daily: %v", err)
	}
	if got := cached.Manifest.Expose[0].PreservePorts[0]; got != 49163 {
		t.Errorf("cached preserve port = %d, want 49163", got)
	}
	cached.State.Close()

	refreshed, err := dailyForWorktreeFresh(info, "oauth", &out, &errOut)
	if err != nil {
		t.Fatalf("refreshed daily: %v", err)
	}
	defer refreshed.State.Close()
	if got := refreshed.Manifest.Expose[0].PreservePorts[0]; got != 49164 {
		t.Errorf("refreshed preserve port = %d, want 49164", got)
	}
	if got := refreshed.Manifest.Env["backend"]["GOOGLE_OAUTH_REDIRECT_URI"]; got != "http://127.0.0.1:49164/callback" {
		t.Errorf("refreshed redirect URI = %q", got)
	}
}

// TestResolveDaily_ForwardsCobraWriters is the end-to-end version of
// the contract above: it actually drives resolveDaily(cmd, slug) from
// inside a real git worktree and asserts the cobra-bound writers
// (cmd.SetOut/SetErr) end up in d.Ctx.Out / d.Ctx.Err. The bug we just
// fixed was that resolveDaily passed os.Stdout/os.Stderr regardless of
// the cobra command, making compose adapter output uncatchable. Without
// the fix this test fails because d.Ctx.Out points at the real os.Stdout
// rather than the buffer the test installed on the command.
func TestResolveDaily_ForwardsCobraWriters(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary required for resolveDaily integration test")
	}

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

	paths, err := infra.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	cfg := &infra.Config{
		Mode:           infra.ModeLocal,
		TLD:            "test",
		BindIP:         "127.0.0.1",
		AnswerIP:       "127.0.0.1",
		TraefikNetwork: "pier",
	}
	if err := cfg.Save(paths); err != nil {
		t.Fatalf("save config: %v", err)
	}

	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		// Stop git from picking up the developer's commit-template /
		// hooks / signing keys when running under the test process.
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "--initial-branch=main")
	manifestBody := `[project]
name = "demo"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"

[[expose]]
service = "web"
port = 3000
`
	if err := os.WriteFile(filepath.Join(repo, ".pier.toml"), []byte(manifestBody), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	runGit("add", "-A")
	runGit("commit", "-m", "init")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	d, err := resolveDaily(cmd, "")
	if err != nil {
		t.Fatalf("resolveDaily: %v", err)
	}
	defer d.State.Close()

	fmt.Fprint(d.Ctx.Out, "OUT-MARKER")
	fmt.Fprint(d.Ctx.Err, "ERR-MARKER")

	if got := out.String(); got != "OUT-MARKER" {
		t.Errorf("resolveDaily did not forward cmd.OutOrStdout to d.Ctx.Out: got %q", got)
	}
	if got := errOut.String(); got != "ERR-MARKER" {
		t.Errorf("resolveDaily did not forward cmd.ErrOrStderr to d.Ctx.Err: got %q", got)
	}
}
