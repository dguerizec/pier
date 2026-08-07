package applied

import (
	"errors"
	"os"
	"testing"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/manifest"
)

func TestSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	c := adapter.Ctx{
		Project:      "demo",
		Slug:         "main",
		WorktreePath: dir,
		Stack:        manifest.Stack{Kind: manifest.KindCompose, File: "compose.yml"},
		ComposeEnv:   map[string]string{"TOKEN": "secret"},
	}
	m := &manifest.Manifest{Project: manifest.Project{Name: "demo"}, Stack: c.Stack}
	state := New(c, dir, "main", m, &adapter.Prepared{
		AdapterData:  []byte("services: {}\n"),
		OverrideData: []byte("services: {}\n"),
	}, &adapter.Handle{ContainerID: "abc"})

	if err := Save(state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path(dir, "main"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state permissions = %o, want 600", got)
	}
	got, err := Load(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "demo" || got.ContainerID != "abc" || got.ComposeEnv["TOKEN"] != "secret" {
		t.Fatalf("loaded state = %+v", got)
	}
	if !got.SameIdentity(c) {
		t.Fatal("saved state should match source identity")
	}
	states, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Slug != "main" {
		t.Fatalf("listed states = %+v", states)
	}
	c.Stack.File = "other.yml"
	if got.SameIdentity(c) {
		t.Fatal("stack.file change should change identity")
	}
	if err := Delete(dir, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, "main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("load after delete = %v, want ErrNotFound", err)
	}
}
