package materialize

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHooks_NoCommands(t *testing.T) {
	if err := RunHooks("post_create", nil, HookContext{}, nil, nil); err != nil {
		t.Fatalf("empty cmds: %v", err)
	}
}

func TestRunHooks_EnvAndCwd(t *testing.T) {
	dir := t.TempDir()
	out := dir + "/out"
	hc := HookContext{
		WorktreePath: dir,
		PrimaryPath:  "/tmp/primary",
		Slug:         "feat-x",
		Branch:       "feat/x",
		BaseDomain:   "myapp.test",
		ProjectName:  "myapp",
	}
	cmd := `printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n' \
		"$PIER_WORKTREE_PATH" "$PIER_PRIMARY_PATH" "$PIER_SLUG" \
		"$PIER_BRANCH" "$PIER_BASE_DOMAIN" "$PIER_PROJECT_NAME" "$(pwd)" > ` + out

	var stdout, stderr bytes.Buffer
	if err := RunHooks("post_create", []string{cmd}, hc, &stdout, &stderr); err != nil {
		t.Fatalf("RunHooks: %v\nstderr=%s", err, stderr.String())
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	want := []string{dir, "/tmp/primary", "feat-x", "feat/x", "myapp.test", "myapp", resolvedDir}
	if len(got) != len(want) {
		t.Fatalf("output lines = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d = %q, want %q", i, got[i], w)
		}
	}
}

func TestRunHooks_StopsOnFirstError(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/marker"
	hc := HookContext{WorktreePath: dir}
	cmds := []string{
		"true",
		"false",
		"touch " + marker,
	}
	var stderr bytes.Buffer
	err := RunHooks("post_create", cmds, hc, nil, &stderr)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "post_create[1]") {
		t.Errorf("err = %v, want index 1", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("third command ran despite earlier failure")
	}
}
