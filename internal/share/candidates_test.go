package share

import (
	"reflect"
	"testing"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/manifest"
)

func testCandidates() []Candidate {
	ctx := adapter.Ctx{
		Project:        "jobo",
		Slug:           "main",
		BaseDomain:     "jobo.lap.test",
		TLD:            "lap.test",
		WorktreePath:   "/work/jobo",
		DefaultService: "frontend",
		Expose: []manifest.ExposeRule{
			{Service: "backend", Port: 8080},
			{Service: "frontend", Host: "frontend", Port: 3000},
		},
	}
	return Candidates(ctx)
}

func TestCandidatesDefaultFirst(t *testing.T) {
	got := testCandidates()
	var hosts []string
	for _, candidate := range got {
		hosts = append(hosts, candidate.Host)
	}
	want := []string{
		"main.jobo.lap.test",
		"backend.main.jobo.lap.test",
		"frontend.main.jobo.lap.test",
	}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("hosts = %v, want %v", hosts, want)
	}
	if !got[0].Default {
		t.Fatal("bare alias should be the default")
	}
	if got[2].Container != "jobo-main-frontend" {
		t.Fatalf("container = %q", got[2].Container)
	}
}

func TestSelectCandidatesExpandsFiniteGlobs(t *testing.T) {
	candidates := testCandidates()
	tests := []struct {
		name      string
		selectors []string
		want      []string
	}{
		{"short host", []string{"backend"}, []string{"backend.main.jobo.lap.test"}},
		{"all", []string{"*"}, candidateHosts(candidates)},
		{"relative project glob", []string{"*.jobo"}, candidateHosts(candidates)},
		{"fqdn project glob", []string{"*.jobo.lap.test"}, candidateHosts(candidates)},
		{"union preserves canonical order", []string{"frontend", "backend"}, []string{
			"backend.main.jobo.lap.test",
			"frontend.main.jobo.lap.test",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectCandidates(candidates, tt.selectors, "lap.test")
			if err != nil {
				t.Fatal(err)
			}
			if hosts := candidateHosts(got); !reflect.DeepEqual(hosts, tt.want) {
				t.Fatalf("hosts = %v, want %v", hosts, tt.want)
			}
		})
	}
}

func TestSelectCandidatesRejectsUnknownAndBadPatterns(t *testing.T) {
	if _, err := SelectCandidates(testCandidates(), []string{"admin"}, "lap.test"); err == nil {
		t.Fatal("unknown selector should fail")
	}
	if _, err := SelectCandidates(testCandidates(), []string{"["}, "lap.test"); err == nil {
		t.Fatal("malformed glob should fail")
	}
}

func candidateHosts(candidates []Candidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.Host)
	}
	return out
}
