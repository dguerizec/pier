package initwizard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseContainerPort(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
	}{
		{"plain string", "3000", 3000},
		{"host:container", "8080:3000", 3000},
		{"env-templated host", "${PORT:-8080}:3000", 3000},
		{"with protocol", "8080:3000/tcp", 3000},
		{"int", 3000, 3000},
		{"long form int target", map[string]any{"target": 3000, "published": 8080}, 3000},
		{"long form string target", map[string]any{"target": "3000"}, 3000},
		{"garbage", "nope", 0},
		{"empty", "", 0},
		{"missing target", map[string]any{"published": 8080}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseContainerPort(c.in); got != c.want {
				t.Errorf("parseContainerPort(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestListComposeServicesWithPorts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	body := `services:
  web:
    image: nginx
    ports:
      - "80:80"
  api:
    image: foo
    ports:
      - "${PORT:-8080}:8000"
  worker:
    image: bar
  db:
    image: postgres
    ports:
      - target: 5432
        published: 5432
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ListComposeServicesWithPorts(path)
	want := []ComposeCandidate{
		{Service: "api", Port: 8000},
		{Service: "db", Port: 5432},
		{Service: "web", Port: 80},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i, c := range got {
		if c != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, c, want[i])
		}
	}
}

func TestDetectComposeFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := DetectComposeFile(dir, ""); err == nil {
		t.Fatal("expected error when no compose file present")
	}

	// dev variant wins over plain
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.dev.yml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DetectComposeFile(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "docker-compose.dev.yml" {
		t.Errorf("got %s, want docker-compose.dev.yml", got)
	}

	// override (relative) resolves against toplevel
	got, err = DetectComposeFile(dir, "compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "compose.yml" {
		t.Errorf("override got %s", got)
	}

	if _, err := DetectComposeFile(dir, "missing.yml"); err == nil {
		t.Error("expected error for missing override")
	}
}
