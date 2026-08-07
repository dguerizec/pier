package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestLsRowJSONIncludesWorktreePath(t *testing.T) {
	body, err := json.Marshal(lsRow{WorktreePath: "/srv/demo/worktrees/feature"})
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	if got := decoded["worktree_path"]; got != "/srv/demo/worktrees/feature" {
		t.Fatalf("worktree_path = %#v, want %q", got, "/srv/demo/worktrees/feature")
	}
}

func TestRenderLsTableCompactOmitsWorktreePath(t *testing.T) {
	out := renderLsTable(t, false)
	if strings.Contains(out, "WORKTREE") || strings.Contains(out, "/srv/demo") {
		t.Fatalf("compact table contains worktree path:\n%s", out)
	}
	if !strings.Contains(out, "PROJECT") || !strings.Contains(out, "demo") {
		t.Fatalf("compact table is missing expected columns:\n%s", out)
	}
}

func TestRenderLsTableWideIncludesWorktreePath(t *testing.T) {
	out := renderLsTable(t, true)
	if !strings.Contains(out, "WORKTREE") || !strings.Contains(out, "/srv/demo") {
		t.Fatalf("wide table is missing worktree path:\n%s", out)
	}
}

func renderLsTable(t *testing.T, wide bool) string {
	t.Helper()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	rows := []lsRow{{
		Project:      "demo",
		Slug:         "main",
		URL:          "http://main.demo.test",
		Status:       "running",
		Uptime:       "1m",
		WorktreePath: "/srv/demo",
	}}
	if err := renderTable(cmd, rows, wide); err != nil {
		t.Fatalf("render table: %v", err)
	}
	return out.String()
}
