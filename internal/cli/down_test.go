package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/applied"
	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/manifest"
	"github.com/dguerizec/pier/internal/state"
	"github.com/dguerizec/pier/internal/worktree"
)

func TestDownUsesAppliedStateWhenCurrentManifestIsInvalid(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("shell-script docker stub is POSIX-only")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	worktreePath := filepath.Join(root, "repo")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, manifest.FileName), []byte("not valid toml = ["), 0o644); err != nil {
		t.Fatal(err)
	}
	installDockerStub(t, root)

	oldManifest := manifest.Manifest{
		Project: manifest.Project{Name: "old-name"},
		Stack:   manifest.Stack{Kind: manifest.KindCompose, File: "old-compose.yml"},
	}
	ctx := adapter.Ctx{Project: "old-name", Slug: "main", WorktreePath: worktreePath, Stack: oldManifest.Stack}
	snapshot := applied.New(ctx, worktreePath, "main", &oldManifest,
		&adapter.Prepared{AdapterData: []byte("services:\n  web:\n    image: pier.local/teardown-placeholder\n")},
		&adapter.Handle{ContainerID: "old-container"})
	if err := applied.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	paths, err := infra.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(&state.Workload{
		Project: "old-name", Slug: "main", WorktreePath: worktreePath, Branch: "main", Kind: manifest.KindCompose,
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	info := &worktree.Info{Toplevel: worktreePath, PrimaryPath: worktreePath, Branch: "main", IsPrimary: true}
	var out, errOut bytes.Buffer
	d, err := dailyForDown(info, "main", &out, &errOut)
	if err != nil {
		t.Fatalf("dailyForDown should not parse current manifest: %v", err)
	}
	defer d.State.Close()
	if d.Applied == nil || d.Ctx.Project != "old-name" {
		t.Fatalf("down context did not use applied identity: %+v", d)
	}
	if err := runDown(d, false, false, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if _, err := applied.Load(worktreePath, "main"); !errors.Is(err, applied.ErrNotFound) {
		t.Fatalf("applied state after down = %v, want ErrNotFound", err)
	}
	if _, err := d.State.Get("old-name", "main"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("workload state after down = %v, want ErrNotFound", err)
	}
	calls := readDockerCalls(t, root)
	if !strings.Contains(calls, "-p old-name-main down --remove-orphans") || strings.Contains(calls, "old-compose.yml") {
		t.Fatalf("down did not use frozen applied config:\n%s", calls)
	}
}

func installDockerStub(t *testing.T, root string) {
	t.Helper()
	stubDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$DOCKER_CALLS"
case "$*" in
  *"config --format=json"*)
    printf '%s\n' '{"services":{"web":{}},"networks":{"default":{"name":"new-name-main_default","external":false},"pier_proxy":{"name":"pier","external":true}}}'
    ;;
  "network ls"*) printf '%s\n' 'pier' ;;
  *"ps -q web"*) printf '%s\n' 'new-container' ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CALLS", filepath.Join(root, "docker-calls"))
}

func readDockerCalls(t *testing.T, root string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "docker-calls"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestRunUpBuildsBeforeReplacingChangedIdentity(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("shell-script docker stub is POSIX-only")
	}
	root := t.TempDir()
	worktreePath := filepath.Join(root, "repo")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "new-compose.yml"), []byte("services:\n  web:\n    image: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installDockerStub(t, root)

	store, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldManifest := manifest.Manifest{
		Project: manifest.Project{Name: "old-name"},
		Stack:   manifest.Stack{Kind: manifest.KindCompose, File: "old-compose.yml"},
	}
	oldCtx := adapter.Ctx{Project: "old-name", Slug: "old-branch", WorktreePath: worktreePath, Stack: oldManifest.Stack}
	old := applied.New(oldCtx, worktreePath, "old-branch", &oldManifest,
		&adapter.Prepared{AdapterData: []byte("services:\n  web:\n    image: pier.local/teardown-placeholder\n")}, nil)
	if err := applied.Save(old); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(&state.Workload{
		Project: "old-name", Slug: "old-branch", WorktreePath: worktreePath, Branch: "old-branch", Kind: manifest.KindCompose,
	}); err != nil {
		t.Fatal(err)
	}

	newManifest := &manifest.Manifest{
		Project: manifest.Project{Name: "new-name", BaseDomain: "new-name.test"},
		Stack:   manifest.Stack{Kind: manifest.KindCompose, File: "new-compose.yml", Service: "web"},
		Expose:  []manifest.ExposeRule{{Service: "web", Port: 3000}},
	}
	var out, errOut bytes.Buffer
	d := &daily{
		Worktree: &worktree.Info{Toplevel: worktreePath, PrimaryPath: worktreePath, Branch: "main", IsPrimary: true},
		Manifest: newManifest,
		Slug:     "main",
		Ctx: adapter.Ctx{
			Project: "new-name", Slug: "main", BaseDomain: "new-name.test", WorktreePath: worktreePath,
			Stack: newManifest.Stack, Expose: newManifest.Expose, DefaultService: "web", TraefikNetwork: "pier",
			Out: io.Discard, Err: io.Discard,
		},
		State: store,
	}
	if err := runUp(d, false, &out, &errOut); err != nil {
		t.Fatalf("runUp: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errOut.String())
	}
	calls := readDockerCalls(t, root)
	buildAt := strings.Index(calls, "-p new-name-main build")
	downAt := strings.Index(calls, "-p old-name-old-branch down --remove-orphans")
	upAt := strings.Index(calls, "-p new-name-main up -d --no-build --remove-orphans --wait")
	if buildAt < 0 || downAt < 0 || upAt < 0 || !(buildAt < downAt && downAt < upAt) {
		t.Fatalf("identity replacement order is not build -> old down -> new up:\n%s", calls)
	}
	got, err := applied.Load(worktreePath, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "new-name" {
		t.Fatalf("applied project = %q, want new-name", got.Project)
	}
	if _, err := store.Get("old-name", "old-branch"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("old state = %v, want ErrNotFound", err)
	}
	if _, err := store.Get("new-name", "main"); err != nil {
		t.Fatalf("new state: %v", err)
	}
}
