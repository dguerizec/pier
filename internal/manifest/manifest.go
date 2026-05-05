// Package manifest parses, validates, and writes .pier.toml files
// described in DESIGN §5.4. Per-developer overrides in .pier.local.toml
// are merged on top of the project manifest.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	FileName       = ".pier.toml"
	LocalFileName  = ".pier.local.toml"
	KindCompose    = "compose"
	KindDockerfile = "dockerfile" // synthesized compose; adapter lands in Phase 3
)

// ErrNotFound signals that no .pier.toml exists at the given root.
var ErrNotFound = errors.New("manifest: .pier.toml not found (run `pier init`)")

// Manifest is the in-memory representation of .pier.toml.
//
// JSON tags mirror the TOML tags so the same struct is the on-disk
// format AND the wire format for the REST API (POST /api/v1/projects).
// Adding a field gives both representations for free; renaming or
// removing one is a breaking change in both surfaces at once — that's
// the trade for not maintaining duplicate DTOs.
type Manifest struct {
	Project     Project                      `toml:"project"               json:"project"`
	Stack       Stack                        `toml:"stack"                 json:"stack"`
	Expose      []ExposeRule                 `toml:"expose"                json:"expose"`
	Env         map[string]map[string]string `toml:"env,omitempty"         json:"env,omitempty"`
	Materialize Materialize                  `toml:"materialize,omitempty" json:"materialize,omitempty"`
	Hooks       Hooks                        `toml:"hooks,omitempty"       json:"hooks,omitempty"`
	Watch       Watch                        `toml:"watch,omitempty"       json:"watch,omitempty"`
	Worktree    Worktree                     `toml:"worktree,omitempty"    json:"worktree,omitempty"`
}

type Project struct {
	Name string `toml:"name" json:"name"`
	// BaseDomain is optional. When empty, pier composes it at runtime as
	// `<name>.<installed-tld>` so the same manifest works across machines
	// where contributors may have installed pier on different TLDs. Set
	// it explicitly only when you need a fixed domain (multi-team setup,
	// custom DNS).
	BaseDomain string `toml:"base_domain,omitempty" json:"base_domain,omitempty"`
}

// Stack covers the supported adapter kinds. Pier is intentionally
// docker-coupled: even one-off raw processes (uv/npm/cargo) are expected
// to declare a docker-compose.dev.yml. The README has a 10-line minimal
// example for projects that don't otherwise containerize.
type Stack struct {
	Kind       string `toml:"kind"                 json:"kind"`
	File       string `toml:"file,omitempty"       json:"file,omitempty"`       // compose
	Dockerfile string `toml:"dockerfile,omitempty" json:"dockerfile,omitempty"` // dockerfile (Phase 3 — synthesized compose)

	// Service names the [[expose]] entry that should also be reachable at
	// the bare `<slug>.<base_domain>` (no sub-domain). When empty or when
	// no [[expose]] matches, no alias is emitted — every exposed service
	// is reachable only via its own `<host>.<slug>.<base_domain>`.
	Service string `toml:"service,omitempty" json:"service,omitempty"`

	// MatchHostUID, when true, makes pier inject `user: "<uid>:<gid>"`
	// into the compose override so the container runs as the host user.
	// Resolves the EACCES class on bind-mounted host paths when the image
	// uses a non-matching default user (typical for distroless/nonroot).
	//
	// Always serialized — `pier init` writes an explicit value so re-reads
	// see the user's choice rather than silently defaulting to false.
	// Hand-edited manifests that omit the field still parse to false (Go
	// zero value), matching the historical behaviour.
	MatchHostUID bool `toml:"match_host_uid" json:"match_host_uid"`
}

// ExposeRule is one service that pier should publish behind traefik.
// Each rule is a service + container port + DNS sub-domain label. The
// resulting URL is `http://<host>.<slug>.<base_domain>`. The service
// pointed at by Stack.Service additionally gets `http://<slug>.<base_domain>`
// as an alias.
type ExposeRule struct {
	Service string `toml:"service" json:"service"`
	Port    int    `toml:"port"    json:"port"`
	// Host is the sub-domain label. Defaults to Service when empty.
	Host string `toml:"host,omitempty" json:"host,omitempty"`
}

// Hostname returns the sub-domain label this rule advertises.
func (e ExposeRule) Hostname() string {
	if e.Host != "" {
		return e.Host
	}
	return e.Service
}

type Materialize struct {
	Symlinks  []string `toml:"symlinks,omitempty"  json:"symlinks,omitempty"`
	Snapshots []string `toml:"snapshots,omitempty" json:"snapshots,omitempty"`
	// PostCreate is a list of shell commands run after symlinks/snapshots
	// have been laid down by `pier worktree add`. Cwd is the new worktree.
	// Each command runs via `sh -c` with PIER_* env vars exposed (see
	// materialize.HookEnv). The first failing command aborts the
	// sequence; the caller decides whether to roll back.
	PostCreate []string `toml:"post_create,omitempty" json:"post_create,omitempty"`
	// PreRemove is a list of shell commands run before `pier worktree rm`
	// hands off to `git worktree remove`. Cwd is the worktree being
	// removed. Same env + sequencing semantics as PostCreate.
	PreRemove []string `toml:"pre_remove,omitempty" json:"pre_remove,omitempty"`
}

type Hooks struct {
	PreUp    string `toml:"pre_up,omitempty"    json:"pre_up,omitempty"`
	PostUp   string `toml:"post_up,omitempty"   json:"post_up,omitempty"`
	PreDown  string `toml:"pre_down,omitempty"  json:"pre_down,omitempty"`
	PostDown string `toml:"post_down,omitempty" json:"post_down,omitempty"`
}

type Watch struct {
	Paths    []string `toml:"paths,omitempty"     json:"paths,omitempty"`
	OnChange string   `toml:"on_change,omitempty" json:"on_change,omitempty"` // rebuild | restart
}

// Worktree configures the `pier worktree add` UX.
type Worktree struct {
	// Dir places new trees here when <name> has no path separator;
	// relative paths resolve against the primary worktree.
	Dir string `toml:"dir,omitempty" json:"dir,omitempty"` // e.g. ".claude/worktrees"
	// BaseRef is the git ref new branches fork from. Defaults to "main"
	// (then "master") when unset; --from on the command line wins.
	BaseRef string `toml:"base_ref,omitempty" json:"base_ref,omitempty"` // e.g. "main"
}

// Load reads <root>/.pier.toml, then layers <root>/.pier.local.toml on top
// if present, and validates the result.
func Load(root string) (*Manifest, error) {
	mainPath := filepath.Join(root, FileName)
	if _, err := os.Stat(mainPath); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}

	m := &Manifest{}
	if _, err := toml.DecodeFile(mainPath, m); err != nil {
		return nil, fmt.Errorf("manifest: parse %s: %w", mainPath, err)
	}

	localPath := filepath.Join(root, LocalFileName)
	if _, err := os.Stat(localPath); err == nil {
		if _, err := toml.DecodeFile(localPath, m); err != nil {
			return nil, fmt.Errorf("manifest: parse %s: %w", localPath, err)
		}
	}

	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Write serializes m as TOML to path. Overwrites any existing file.
//
// The default BurntSushi/toml encoder emits an explicit parent header
// for every map-of-map field (e.g. `[env]` followed by an indented
// `[env.front]`). Both forms parse identically, but the explicit
// parent generates noisy diffs against hand-written manifests that use
// the dotted form. We rewrite the output to drop empty parent headers
// before persisting.
func (m *Manifest) Write(path string) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return err
	}
	return os.WriteFile(path, collapseEmptyParents(buf.Bytes()), 0o644)
}

// emptyParentRE matches a top-level `[name]` header on its own line.
// We use it to detect candidates for collapsing; the actual decision
// also looks ahead at the next non-blank line.
var emptyParentRE = regexp.MustCompile(`^\[([A-Za-z0-9_-]+)\]$`)

// collapseEmptyParents removes `[parent]` headers that are immediately
// followed only by indented `[parent.child]` sub-tables, and dedents the
// block by two spaces so the result matches the dotted-table style a
// human would write by hand.
func collapseEmptyParents(in []byte) []byte {
	lines := strings.Split(string(in), "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		m := emptyParentRE.FindStringSubmatch(lines[i])
		if m == nil {
			out = append(out, lines[i])
			continue
		}
		parent := m[1]
		// Look at the next non-blank line.
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) || !strings.HasPrefix(strings.TrimLeft(lines[j], " \t"), "["+parent+".") {
			out = append(out, lines[i])
			continue
		}
		// Drop the `[parent]` line and dedent the block (everything
		// until the next top-level header or EOF) by two spaces.
		for k := i + 1; k < len(lines); k++ {
			line := lines[k]
			trimmed := strings.TrimLeft(line, " \t")
			if trimmed != "" && strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				// Reached the next top-level header; resume normal copy from here.
				out = append(out, lines[i+1:k]...)
				dedent(out[len(out)-(k-i-1):])
				i = k - 1
				goto next
			}
		}
		// Block runs to EOF.
		out = append(out, lines[i+1:]...)
		dedent(out[len(out)-(len(lines)-i-1):])
		i = len(lines)
	next:
	}
	return []byte(strings.Join(out, "\n"))
}

// dedent strips up to two leading spaces from every non-empty line in
// place. Used after dropping a parent header so sub-tables move flush
// left.
func dedent(block []string) {
	for i, line := range block {
		switch {
		case strings.HasPrefix(line, "  "):
			block[i] = line[2:]
		case strings.HasPrefix(line, "\t"):
			block[i] = line[1:]
		}
	}
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Validate checks that required fields are set and consistent with Stack.Kind.
//
// Stack.Service is intentionally not cross-checked against [[expose]]: if
// it points at a missing service, the runtime simply skips the bare-slug
// alias rather than fail. That matches the user's mental model — Service
// is a hint, not a hard reference.
func (m *Manifest) Validate() error {
	if m.Project.Name == "" {
		return errors.New("manifest: project.name is required")
	}
	if !dnsLabel.MatchString(m.Project.Name) {
		return fmt.Errorf("manifest: project.name %q is not a valid DNS label", m.Project.Name)
	}
	// project.base_domain is optional — empty means pier composes it from
	// the installed TLD at runtime. Validation only checks the format
	// when set.

	switch m.Stack.Kind {
	case KindCompose:
		if m.Stack.File == "" {
			return errors.New("manifest: stack.file is required for kind=compose")
		}
	case KindDockerfile:
		if m.Stack.Dockerfile == "" {
			return errors.New("manifest: stack.dockerfile is required for kind=dockerfile")
		}
	case "":
		return errors.New("manifest: stack.kind is required")
	default:
		return fmt.Errorf("manifest: stack.kind %q must be compose (or dockerfile, Phase 3)", m.Stack.Kind)
	}

	if len(m.Expose) == 0 {
		return errors.New("manifest: at least one [[expose]] entry is required")
	}
	seenService := map[string]bool{}
	seenHost := map[string]bool{}
	for i, e := range m.Expose {
		if e.Service == "" {
			return fmt.Errorf("manifest: expose[%d].service is required", i)
		}
		if seenService[e.Service] {
			return fmt.Errorf("manifest: expose: service %q listed twice", e.Service)
		}
		seenService[e.Service] = true
		if e.Port <= 0 {
			return fmt.Errorf("manifest: expose[%d].port must be > 0", i)
		}
		host := e.Hostname()
		if !dnsLabel.MatchString(host) {
			return fmt.Errorf("manifest: expose[%d].host %q is not a valid DNS label", i, host)
		}
		if seenHost[host] {
			return fmt.Errorf("manifest: expose: host %q listed twice", host)
		}
		seenHost[host] = true
	}

	if oc := m.Watch.OnChange; oc != "" && oc != "rebuild" && oc != "restart" {
		return fmt.Errorf("manifest: watch.on_change %q must be rebuild or restart", oc)
	}
	return nil
}

// DefaultExpose returns the [[expose]] entry that should also be reachable
// at the bare `<slug>.<base_domain>` alias, or nil when no alias should
// be emitted (Stack.Service empty or pointing at a missing service).
func (m *Manifest) DefaultExpose() *ExposeRule {
	if m.Stack.Service == "" {
		return nil
	}
	for i := range m.Expose {
		if m.Expose[i].Service == m.Stack.Service {
			return &m.Expose[i]
		}
	}
	return nil
}
