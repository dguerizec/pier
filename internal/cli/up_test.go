package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dguerizec/pier/internal/manifest"
	"github.com/dguerizec/pier/internal/state"
	"github.com/dguerizec/pier/internal/worktree"
)

func TestRegisterProjectForUpUsesPrimaryWorktree(t *testing.T) {
	store, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d := &daily{
		Worktree: &worktree.Info{
			Toplevel:    "/repos/demo-worktrees/feature",
			PrimaryPath: "/repos/demo",
		},
		Manifest: &manifest.Manifest{Project: manifest.Project{Name: "demo"}},
		State:    store,
	}
	var errOut bytes.Buffer

	registerProjectForUp(d, &errOut)
	registerProjectForUp(d, &errOut)

	if errOut.Len() != 0 {
		t.Fatalf("idempotent registration warned: %s", errOut.String())
	}
	got, err := store.GetProject("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepoPath != "/repos/demo" {
		t.Fatalf("repo path = %q, want primary /repos/demo", got.RepoPath)
	}
}

func TestRegisterProjectForUpWarnsAndContinuesOnConflict(t *testing.T) {
	store, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.RegisterProject("demo", "/repos/old-demo"); err != nil {
		t.Fatal(err)
	}

	d := &daily{
		Worktree: &worktree.Info{
			Toplevel:    "/repos/demo",
			PrimaryPath: "/repos/demo",
		},
		Manifest: &manifest.Manifest{Project: manifest.Project{Name: "demo"}},
		State:    store,
	}
	var errOut bytes.Buffer

	registerProjectForUp(d, &errOut)

	if !strings.Contains(errOut.String(), "state: project already registered") {
		t.Fatalf("conflict warning = %q", errOut.String())
	}
	got, err := store.GetProject("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepoPath != "/repos/old-demo" {
		t.Fatalf("conflict overwrote repo path with %q", got.RepoPath)
	}
}
