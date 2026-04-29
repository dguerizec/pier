package headscale

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAdd_NewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns_records.json")
	added, err := Add(path, "foo.crops.nebula", "100.64.0.10")
	if err != nil || !added {
		t.Fatalf("Add: %v added=%v", err, added)
	}
	records, err := List(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != "foo.crops.nebula" || records[0].Value != "100.64.0.10" {
		t.Errorf("records = %+v", records)
	}
}

func TestAdd_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns_records.json")
	if _, err := Add(path, "x.test", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	added, err := Add(path, "x.test", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("re-Add with same value should report added=false")
	}
}

func TestAdd_Conflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns_records.json")
	if _, err := Add(path, "x.test", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	_, err := Add(path, "x.test", "9.9.9.9")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns_records.json")
	_, _ = Add(path, "a.test", "1.2.3.4")
	_, _ = Add(path, "b.test", "1.2.3.4")

	removed, err := Remove(path, "a.test")
	if err != nil || !removed {
		t.Fatalf("Remove: %v removed=%v", err, removed)
	}
	records, _ := List(path)
	if len(records) != 1 || records[0].Name != "b.test" {
		t.Errorf("after remove: %+v", records)
	}

	// Idempotent
	removed, err = Remove(path, "a.test")
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("removing missing entry should report removed=false")
	}
}

func TestPreservesUnrelated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns_records.json")
	prior := `[
  {"name":"traefik.nebula","type":"A","value":"100.64.0.10"},
  {"name":"headscale.nebula","type":"A","value":"100.64.0.10"}
]
`
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(path, "feat-x.crops.nebula", "100.64.0.10"); err != nil {
		t.Fatal(err)
	}
	records, _ := List(path)
	if len(records) != 3 {
		t.Errorf("expected 3 records, got %d: %+v", len(records), records)
	}
	// Original two must survive
	names := map[string]bool{}
	for _, r := range records {
		names[r.Name] = true
	}
	for _, want := range []string{"traefik.nebula", "headscale.nebula", "feat-x.crops.nebula"} {
		if !names[want] {
			t.Errorf("missing %s after Add", want)
		}
	}
}

func TestConcurrentAdds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns_records.json")

	// 10 concurrent agents each adding a distinct slug; flock must serialize.
	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := slugName(i)
			if _, err := Add(path, name, "100.64.0.10"); err != nil {
				t.Errorf("Add(%s): %v", name, err)
			}
		}()
	}
	wg.Wait()

	records, err := List(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != N {
		t.Errorf("expected %d records after concurrent adds, got %d", N, len(records))
	}
}

func TestHas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns_records.json")
	if has, _ := Has(path, "x.test"); has {
		t.Error("Has on empty file should be false")
	}
	_, _ = Add(path, "x.test", "1.2.3.4")
	if has, _ := Has(path, "x.test"); !has {
		t.Error("Has after Add should be true")
	}
}

func slugName(i int) string {
	return string(rune('a'+i)) + ".crops.nebula"
}
