package headscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnpatch_RoundtripWithPatched(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `server_url: http://example.org

dns:
  base_domain: nebula
  nameservers:
    global:
      - 1.1.1.1

log:
  level: info
`)
	if _, err := Patch(cfg, "test", "100.64.0.10"); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	changed, err := Unpatch(cfg, "test", "100.64.0.10")
	if err != nil {
		t.Fatalf("Unpatch: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	out, _ := os.ReadFile(cfg)
	body := string(out)

	// pier additions gone
	if strings.Contains(body, "split:") {
		t.Errorf("split: still present after unpatch:\n%s", body)
	}
	if strings.Contains(body, "100.64.0.10") {
		t.Errorf("ip still present after unpatch:\n%s", body)
	}
	if strings.Contains(body, "search_domains:") {
		t.Errorf("search_domains: still present (was created by Patch, should be removed):\n%s", body)
	}

	// pre-existing keys survive
	for _, want := range []string{
		"base_domain: nebula",
		"global:",
		"- 1.1.1.1",
		"server_url: http://example.org",
		"log:",
		"level: info",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing pre-existing %q in:\n%s", want, body)
		}
	}
}

func TestUnpatch_IdempotentNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	original := `dns:
  base_domain: nebula
  nameservers:
    global:
      - 1.1.1.1
`
	writeFile(t, cfg, original)

	changed, err := Unpatch(cfg, "test", "100.64.0.10")
	if err != nil {
		t.Fatalf("Unpatch: %v", err)
	}
	if changed {
		t.Error("changed = true on a config without pier rule; want false")
	}
	body, _ := os.ReadFile(cfg)
	if string(body) != original {
		t.Errorf("file mutated despite no-op:\nwant:\n%s\ngot:\n%s", original, body)
	}
}

func TestUnpatch_PreservesOtherSplitTLDs(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `dns:
  nameservers:
    split:
      foo:
        - 1.2.3.4
      test:
        - 100.64.0.10
  search_domains:
    - foo
    - test
`)

	changed, err := Unpatch(cfg, "test", "100.64.0.10")
	if err != nil {
		t.Fatalf("Unpatch: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true")
	}
	body, _ := os.ReadFile(cfg)
	s := string(body)

	// removed
	if strings.Contains(s, "test:") || strings.Contains(s, "100.64.0.10") {
		t.Errorf("test/ip still present:\n%s", s)
	}
	if strings.Contains(s, "- test") {
		t.Errorf("- test still in search_domains:\n%s", s)
	}

	// preserved
	for _, want := range []string{"foo:", "1.2.3.4", "- foo", "split:", "search_domains:"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing preserved %q in:\n%s", want, s)
		}
	}
}

func TestUnpatch_DropsEmptySplitMap(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `dns:
  nameservers:
    global:
      - 1.1.1.1
    split:
      test:
        - 100.64.0.10
`)

	if _, err := Unpatch(cfg, "test", "100.64.0.10"); err != nil {
		t.Fatalf("Unpatch: %v", err)
	}
	body, _ := os.ReadFile(cfg)
	s := string(body)

	// split: gone (was the only entry)
	if strings.Contains(s, "split:") {
		t.Errorf("empty split map should have been dropped:\n%s", s)
	}
	// nameservers and global preserved
	for _, want := range []string{"nameservers:", "global:", "1.1.1.1"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

func TestUnpatch_PreservesUnrelatedDNSKeys(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `dns:
  base_domain: nebula
  magic_dns: true
  override_local_dns: true
  nameservers:
    split:
      test:
        - 100.64.0.10
  search_domains:
    - test
  extra_records_path: /etc/headscale/dns_records.json
`)

	if _, err := Unpatch(cfg, "test", "100.64.0.10"); err != nil {
		t.Fatalf("Unpatch: %v", err)
	}
	body, _ := os.ReadFile(cfg)
	s := string(body)

	for _, want := range []string{
		"base_domain: nebula",
		"magic_dns: true",
		"override_local_dns: true",
		"extra_records_path: /etc/headscale/dns_records.json",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("clobbered unrelated dns key %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "test:") || strings.Contains(s, "100.64.0.10") {
		t.Errorf("pier rule not removed:\n%s", s)
	}
}

func TestUnpatch_PartialPresence_SplitOnly(t *testing.T) {
	// User manually removed search_domains but split entry remains.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `dns:
  nameservers:
    split:
      test:
        - 100.64.0.10
`)

	changed, err := Unpatch(cfg, "test", "100.64.0.10")
	if err != nil {
		t.Fatalf("Unpatch: %v", err)
	}
	if !changed {
		t.Error("changed = false despite split entry present")
	}
	body, _ := os.ReadFile(cfg)
	if strings.Contains(string(body), "100.64.0.10") {
		t.Errorf("split entry not removed:\n%s", body)
	}
}

func TestUnpatch_PartialPresence_SearchOnly(t *testing.T) {
	// User manually removed split entry but search_domains still has tld.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `dns:
  search_domains:
    - test
`)

	changed, err := Unpatch(cfg, "test", "100.64.0.10")
	if err != nil {
		t.Fatalf("Unpatch: %v", err)
	}
	if !changed {
		t.Error("changed = false despite search_domains entry present")
	}
	body, _ := os.ReadFile(cfg)
	if strings.Contains(string(body), "- test") {
		t.Errorf("search_domains entry not removed:\n%s", body)
	}
}

func TestUnpatch_KeepsExtraIPInList(t *testing.T) {
	// User manually added a second IP to the split list — only ours goes.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeFile(t, cfg, `dns:
  nameservers:
    split:
      test:
        - 100.64.0.10
        - 192.168.1.1
  search_domains:
    - test
`)

	if _, err := Unpatch(cfg, "test", "100.64.0.10"); err != nil {
		t.Fatalf("Unpatch: %v", err)
	}
	body, _ := os.ReadFile(cfg)
	s := string(body)
	if strings.Contains(s, "100.64.0.10") {
		t.Errorf("our IP still present:\n%s", s)
	}
	if !strings.Contains(s, "192.168.1.1") {
		t.Errorf("user IP wrongly removed:\n%s", s)
	}
	// tld key kept since list non-empty
	if !strings.Contains(s, "test:") {
		t.Errorf("test key dropped despite remaining IP:\n%s", s)
	}
}
