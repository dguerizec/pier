package state

import (
	"errors"
	"testing"
	"time"
)

func TestUpsertAndGetProject(t *testing.T) {
	s := mustOpen(t)
	p := &Project{
		Name:         "estibador",
		Path:         "/code/estibador",
		BaseDomain:   "estibador.test",
		StackFile:    "docker-compose.dev.yml",
		StackService: "estibador",
		LastSeen:     time.Unix(1700000000, 0),
	}
	if err := s.UpsertProject(p); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	got, err := s.GetProject("estibador")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Path != p.Path || got.BaseDomain != p.BaseDomain ||
		got.StackFile != p.StackFile || got.StackService != p.StackService {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
	if !got.LastSeen.Equal(p.LastSeen) {
		t.Fatalf("LastSeen: want %v got %v", p.LastSeen, got.LastSeen)
	}
}

func TestUpsertProject_DefaultsLastSeen(t *testing.T) {
	s := mustOpen(t)
	p := &Project{Name: "n", Path: "/p", BaseDomain: "n.test", StackFile: "compose.yml"}
	before := time.Now().Add(-time.Second)
	if err := s.UpsertProject(p); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if p.LastSeen.Before(before) {
		t.Fatalf("LastSeen was not set: %v", p.LastSeen)
	}
}

func TestUpsertProject_RefreshesOnConflict(t *testing.T) {
	s := mustOpen(t)
	first := &Project{
		Name: "p", Path: "/old", BaseDomain: "p.test",
		StackFile: "compose.yml", LastSeen: time.Unix(1, 0),
	}
	if err := s.UpsertProject(first); err != nil {
		t.Fatalf("UpsertProject 1: %v", err)
	}
	second := &Project{
		Name: "p", Path: "/new", BaseDomain: "p2.test",
		StackFile: "compose.dev.yml", LastSeen: time.Unix(2, 0),
	}
	if err := s.UpsertProject(second); err != nil {
		t.Fatalf("UpsertProject 2: %v", err)
	}
	got, err := s.GetProject("p")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Path != "/new" || got.BaseDomain != "p2.test" || got.StackFile != "compose.dev.yml" {
		t.Fatalf("expected refresh, got %+v", got)
	}
}

func TestGetProject_NotFound(t *testing.T) {
	s := mustOpen(t)
	_, err := s.GetProject("missing")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("want ErrProjectNotFound, got %v", err)
	}
}

func TestListProjects(t *testing.T) {
	s := mustOpen(t)
	for _, n := range []string{"b", "a", "c"} {
		if err := s.UpsertProject(&Project{
			Name: n, Path: "/" + n, BaseDomain: n + ".test",
			StackFile: "compose.yml",
		}); err != nil {
			t.Fatalf("UpsertProject %s: %v", n, err)
		}
	}
	list, err := s.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 3 || list[0].Name != "a" || list[1].Name != "b" || list[2].Name != "c" {
		t.Fatalf("expected sorted [a b c], got %+v", list)
	}
}

func TestDeleteProject(t *testing.T) {
	s := mustOpen(t)
	if err := s.UpsertProject(&Project{
		Name: "p", Path: "/p", BaseDomain: "p.test", StackFile: "c.yml",
	}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if err := s.DeleteProject("p"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetProject("p"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("want ErrProjectNotFound after delete, got %v", err)
	}
	// no-op when absent
	if err := s.DeleteProject("p"); err != nil {
		t.Fatalf("DeleteProject (idempotent): %v", err)
	}
}
