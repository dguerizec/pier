package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserDirUsesNeutralAgentsLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := UserDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".agents", "skills", "pier")
	if got != want {
		t.Fatalf("UserDir() = %q, want %q", got, want)
	}
}

func TestDetectedLinkTargetsOnlyExistingParents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-home"))

	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "codex-home", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := DetectedLinkTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(targets) = %d, want 2: %+v", len(got), got)
	}
	if got[0].Agent != "claude" || got[0].Dir != filepath.Join(home, ".claude", "skills", "pier") {
		t.Fatalf("claude target = %+v", got[0])
	}
	if got[1].Agent != "codex" || got[1].Dir != filepath.Join(home, "codex-home", "skills", "pier") {
		t.Fatalf("codex target = %+v", got[1])
	}
}

func TestLinkState(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, ".agents", "skills", "pier")
	target := filepath.Join(dir, ".claude", "skills", "pier")

	status, err := LinkState(target, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if status != LinkMissing {
		t.Fatalf("missing status = %v, want %v", status, LinkMissing)
	}

	if err := Link(target, canonical); err != nil {
		t.Fatal(err)
	}
	status, err = LinkState(target, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if status != LinkCurrent {
		t.Fatalf("current status = %v, want %v", status, LinkCurrent)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err = LinkState(target, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if status != LinkConflict {
		t.Fatalf("conflict status = %v, want %v", status, LinkConflict)
	}
}
