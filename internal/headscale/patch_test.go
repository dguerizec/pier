package headscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPatch_AppendsToEmptyDNS(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `# top of file
server_url: http://example.org

dns:
  base_domain: nebula
  nameservers:
    global:
      - 1.1.1.1

log:
  level: info
`)

	changed, err := Patch(cfg, "test", "100.64.0.10")
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true")
	}
	out, _ := os.ReadFile(cfg)
	body := string(out)
	for _, want := range []string{
		"split:",
		"test:",
		"100.64.0.10",
		"search_domains:",
		"- test",
		// pre-existing keys must survive
		"base_domain: nebula",
		"global:",
		"- 1.1.1.1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	// .bak created with original content
	bak, _ := os.ReadFile(cfg + ".bak")
	if !strings.Contains(string(bak), "base_domain: nebula") || strings.Contains(string(bak), "100.64.0.10") {
		t.Error(".bak does not contain pristine original")
	}
}

func TestPatch_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, "dns:\n  nameservers:\n    split:\n      test:\n        - 100.64.0.10\n  search_domains:\n    - test\n")

	changed, err := Patch(cfg, "test", "100.64.0.10")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-patching with the same values reported changed=true; should be a no-op")
	}
}

func TestPatch_AddsNewTLDAlongsideExisting(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `dns:
  nameservers:
    split:
      foo:
        - 1.2.3.4
  search_domains:
    - foo
`)
	if _, err := Patch(cfg, "test", "100.64.0.10"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(cfg)
	s := string(body)
	for _, want := range []string{
		"foo:", "1.2.3.4", "test:", "100.64.0.10", "- foo", "- test",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in result:\n%s", want, s)
		}
	}
}
