package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillInstallOnlyWritesBundledSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))

	cmd := newSkillCmd()
	cmd.SetArgs([]string{"install", "--yes"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, ".agents", "skills", "pier", "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "pier skill install --yes") {
		t.Fatalf("installed skill does not document its refresh command: %s", path)
	}
	if !strings.Contains(out.String(), "AI skill installed") {
		t.Fatalf("output = %q, want install confirmation", out.String())
	}
}
