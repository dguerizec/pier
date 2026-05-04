package state

import (
	"errors"
	"testing"
	"time"
)

func mustOpen(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertAndGet_Compose(t *testing.T) {
	s := mustOpen(t)
	w := &Workload{
		Project:      "myapp",
		Slug:         "feat-x",
		WorktreePath: "/tmp/myapp-feat-x",
		Branch:       "feat/x",
		Kind:         "compose",
		ContainerID:  "abc123",
		StartedAt:    time.Unix(1700000000, 0),
	}
	if err := s.Upsert(w); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.Get("myapp", "feat-x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ContainerID != "abc123" || got.Kind != "compose" {
		t.Errorf("got = %+v", got)
	}
	if !got.StartedAt.Equal(w.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, w.StartedAt)
	}
	if got.PID != 0 || got.Port != 0 {
		t.Errorf("expected nullable pid/port to round-trip as zero, got pid=%d port=%d", got.PID, got.Port)
	}
}

func TestUpsertAndGet_Process(t *testing.T) {
	s := mustOpen(t)
	w := &Workload{
		Project: "uvapp", Slug: "dev",
		WorktreePath: "/tmp/uvapp", Branch: "main", Kind: "process",
		PID: 4242, Port: 5173,
	}
	if err := s.Upsert(w); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Get("uvapp", "dev")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PID != 4242 || got.Port != 5173 {
		t.Errorf("got = %+v", got)
	}
	if got.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty", got.ContainerID)
	}
}

func TestUpsert_Replaces(t *testing.T) {
	s := mustOpen(t)
	first := &Workload{
		Project: "p", Slug: "s",
		WorktreePath: "/old", Branch: "main", Kind: "compose",
		ContainerID: "old",
	}
	if err := s.Upsert(first); err != nil {
		t.Fatal(err)
	}
	second := *first
	second.WorktreePath = "/new"
	second.ContainerID = "new"
	if err := s.Upsert(&second); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("p", "s")
	if got.WorktreePath != "/new" || got.ContainerID != "new" {
		t.Errorf("Upsert did not replace: got = %+v", got)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := mustOpen(t)
	_, err := s.Get("nope", "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestListOrder(t *testing.T) {
	s := mustOpen(t)
	rows := []*Workload{
		{Project: "b", Slug: "z", Branch: "main", Kind: "compose", WorktreePath: "/b/z"},
		{Project: "a", Slug: "y", Branch: "main", Kind: "compose", WorktreePath: "/a/y"},
		{Project: "a", Slug: "x", Branch: "main", Kind: "compose", WorktreePath: "/a/x"},
	}
	for _, r := range rows {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"a", "x"}, {"a", "y"}, {"b", "z"}}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range got {
		if w.Project != want[i][0] || w.Slug != want[i][1] {
			t.Errorf("row %d = %s/%s, want %s/%s", i, w.Project, w.Slug, want[i][0], want[i][1])
		}
	}
}

func TestRegisterAndGetProject(t *testing.T) {
	s := mustOpen(t)
	p, err := s.RegisterProject("myapp", "/home/me/dev/myapp")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if p.Name != "myapp" || p.RepoPath != "/home/me/dev/myapp" {
		t.Errorf("got %+v", p)
	}
	if p.RegisteredAt.IsZero() {
		t.Error("RegisteredAt unset")
	}

	got, err := s.GetProject("myapp")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.RepoPath != "/home/me/dev/myapp" {
		t.Errorf("got %+v", got)
	}

	gotByRepo, err := s.GetProjectByRepo("/home/me/dev/myapp")
	if err != nil {
		t.Fatalf("GetProjectByRepo: %v", err)
	}
	if gotByRepo.Name != "myapp" {
		t.Errorf("got %+v", gotByRepo)
	}
}

func TestRegisterProjectIdempotentOnSameRepo(t *testing.T) {
	// Re-registering the SAME (name, repo_path) is a no-op — `pier init`
	// must be safe to re-run.
	s := mustOpen(t)
	first, err := s.RegisterProject("app", "/repo")
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	second, err := s.RegisterProject("app", "/repo")
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if !first.RegisteredAt.Equal(second.RegisteredAt) {
		t.Errorf("re-registration bumped RegisteredAt: %v -> %v",
			first.RegisteredAt, second.RegisteredAt)
	}
}

func TestRegisterProjectConflicts(t *testing.T) {
	s := mustOpen(t)
	if _, err := s.RegisterProject("app", "/repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterProject("app", "/different-repo"); !errors.Is(err, ErrProjectExists) {
		t.Errorf("name conflict: err = %v, want ErrProjectExists", err)
	}
	if _, err := s.RegisterProject("different-name", "/repo"); !errors.Is(err, ErrProjectExists) {
		t.Errorf("repo conflict: err = %v, want ErrProjectExists", err)
	}
}

func TestProjectNotFound(t *testing.T) {
	s := mustOpen(t)
	if _, err := s.GetProject("nope"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("GetProject: err = %v, want ErrProjectNotFound", err)
	}
	if _, err := s.GetProjectByRepo("/nope"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("GetProjectByRepo: err = %v, want ErrProjectNotFound", err)
	}
}

func TestListProjectsOrder(t *testing.T) {
	s := mustOpen(t)
	for _, p := range []struct{ name, repo string }{
		{"charlie", "/c"},
		{"alpha", "/a"},
		{"bravo", "/b"},
	} {
		if _, err := s.RegisterProject(p.name, p.repo); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "bravo", "charlie"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, p := range got {
		if p.Name != want[i] {
			t.Errorf("row %d = %s, want %s", i, p.Name, want[i])
		}
	}
}

func TestUnregisterProject(t *testing.T) {
	s := mustOpen(t)
	if _, err := s.RegisterProject("app", "/repo"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnregisterProject("app"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject("app"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("after unregister: err = %v, want ErrProjectNotFound", err)
	}
	// idempotent
	if err := s.UnregisterProject("app"); err != nil {
		t.Errorf("unregister on missing row should be no-op, got %v", err)
	}
}

func TestUnregisterProjectKeepsWorkloads(t *testing.T) {
	// Unregistering a project must NOT cascade to workloads — the user's
	// running envs survive the registry forgetting where the repo lives.
	s := mustOpen(t)
	if _, err := s.RegisterProject("app", "/repo"); err != nil {
		t.Fatal(err)
	}
	w := &Workload{
		Project: "app", Slug: "feat", Branch: "feat", Kind: "compose",
		WorktreePath: "/repo/.pier/worktrees/feat",
	}
	if err := s.Upsert(w); err != nil {
		t.Fatal(err)
	}
	if err := s.UnregisterProject("app"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("app", "feat"); err != nil {
		t.Errorf("workload disappeared after unregister: %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := mustOpen(t)
	w := &Workload{Project: "p", Slug: "s", Branch: "main", Kind: "compose", WorktreePath: "/p/s"}
	_ = s.Upsert(w)

	if err := s.Delete("p", "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("p", "s"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete, err = %v, want ErrNotFound", err)
	}
	// idempotent
	if err := s.Delete("p", "s"); err != nil {
		t.Errorf("delete on missing row should be no-op, got %v", err)
	}
}
