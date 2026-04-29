package initwizard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/LeoPartt/pier/internal/manifest"
)

// Opts is the wizard's parameter bag, populated by CLI flags. Empty
// strings mean "use the derived default".
type Opts struct {
	Name        string
	Domain      string
	Service     string
	File        string
	Private     bool
	Yes         bool
	WorktreeDir string
	BaseRef     string
}

// Plan is a fully populated proposal for a new .pier.toml. Derive fills
// every field with a sane default; the optional prompt step then mutates
// only the fields the user couldn't decide automatically.
type Plan struct {
	Toplevel     string
	ManifestPath string

	Name        string
	Domain      string
	ComposeFile string // absolute
	Candidates  []ComposeCandidate
	// Selected indexes into Candidates for the services we will expose.
	// Pre-filled with every candidate (or the existing exposes on re-init);
	// PromptHuh lets the user toggle individual ones off when there are
	// multiple.
	Selected       []bool
	DefaultService string // name of the service for the bare-slug alias; "" disables the alias
	WorktreeDir    string
	BaseRef        string
	Share          bool

	// Existing is the previous manifest when re-running pier init on a
	// project that already has a .pier.toml. Apply uses it as the base for
	// the rewrite so user-curated sections (env, materialize, hooks, watch,
	// stack.match_host_uid) survive untouched. Nil on first init.
	Existing *manifest.Manifest

	// EnvSuggestions are templatisable env values discovered in the
	// compose file. Aligned with EnvAccepted: only suggestions whose flag
	// is true are written to the manifest.
	EnvSuggestions []EnvSuggestion
	EnvAccepted    []bool
}

// IsReinit reports whether the wizard is editing an existing manifest
// rather than creating one from scratch.
func (p *Plan) IsReinit() bool { return p.Existing != nil }

// AmbiguityKind enumerates the reasons we ask the user for input.
type AmbiguityKind int

const (
	// AmbInvalidName is set when the derived project name (from the
	// directory basename) failed DNS-label validation.
	AmbInvalidName AmbiguityKind = iota
	// AmbExpose is set when the compose file declares multiple services
	// with published ports and the user might want to opt some out.
	AmbExpose
	// AmbDefaultService is set when at least two services will be exposed
	// and the user didn't pin --service.
	AmbDefaultService
	// AmbEnvSuggestions is set when the compose file contains environment
	// values that look like cross-service URLs we can templatise.
	AmbEnvSuggestions
)

// Ambiguity flags a Plan field that took a default but a human might
// want to revise. Prompt iterates over these to render the form.
type Ambiguity struct {
	Kind    AmbiguityKind
	Message string
}

// Derive resolves every Plan field from the toplevel + opts. It never
// prompts. Errors signal hard failures (no compose file, manifest
// already exists, no published ports at all). Soft choices come back as
// Ambiguity entries so the caller can decide whether to prompt.
func Derive(toplevel string, opts Opts) (*Plan, []Ambiguity, error) {
	manifestPath := filepath.Join(toplevel, manifest.FileName)

	// Re-init: load the existing manifest as the new defaults source. We
	// decode .pier.toml directly (not via manifest.Load) so we don't bake
	// .pier.local.toml overrides into the shared file when we rewrite it.
	var existing *manifest.Manifest
	if _, err := os.Stat(manifestPath); err == nil {
		existing = &manifest.Manifest{}
		if _, err := toml.DecodeFile(manifestPath, existing); err != nil {
			return nil, nil, fmt.Errorf("re-init: parse %s: %w", manifestPath, err)
		}
	}

	// File: --file > existing stack.file > auto-detect.
	fileHint := opts.File
	if fileHint == "" && existing != nil {
		fileHint = existing.Stack.File
	}
	composeFile, err := DetectComposeFile(toplevel, fileHint)
	if err != nil {
		return nil, nil, err
	}
	candidates := ListComposeServicesWithPorts(composeFile)
	if len(candidates) == 0 {
		return nil, nil, errors.New("no service with a published port detected; add `ports:` to at least one service in the compose file before running pier init")
	}

	var ambig []Ambiguity

	name := firstNonEmpty(opts.Name, existingName(existing), Slugify(filepath.Base(toplevel)))
	if err := ValidateName(name); err != nil {
		ambig = append(ambig, Ambiguity{Kind: AmbInvalidName, Message: err.Error()})
	}

	domain := firstNonEmpty(opts.Domain, existingDomain(existing))
	if domain == "" {
		// {pier.tld} expands at runtime so the same manifest works on
		// hosts that installed pier under different TLDs.
		domain = name + ".{pier.tld}"
	}

	selected := initialSelection(candidates, existing)
	if len(candidates) > 1 {
		ambig = append(ambig, Ambiguity{
			Kind:    AmbExpose,
			Message: fmt.Sprintf("%d services have published ports; pick which to expose", len(candidates)),
		})
	}

	defaultService := firstNonEmpty(opts.Service, existingService(existing))
	if defaultService == "" {
		// Fall back to the first selected candidate, then the first
		// candidate overall (in case nothing is selected on re-init).
		if first := firstSelectedService(candidates, selected); first != "" {
			defaultService = first
		} else {
			defaultService = candidates[0].Service
		}
	}
	// Only flag the default-service question when we actually have a
	// meaningful choice (≥2 services AND the user didn't pin --service).
	if opts.Service == "" && len(candidates) > 1 {
		ambig = append(ambig, Ambiguity{
			Kind:    AmbDefaultService,
			Message: "which service gets the bare <slug>.<base_domain> alias?",
		})
	}

	worktreeDir := firstNonEmpty(opts.WorktreeDir, existingWorktreeDir(existing), ".pier/worktrees")
	baseRef := firstNonEmpty(opts.BaseRef, existingBaseRef(existing), DetectDefaultBranch(toplevel))

	envSuggestions := ScanEnvSuggestions(composeFile, existing)
	// Suggestions are off by default: rewriting a service's env behind the
	// user's back is too sharp an edge for --yes. The interactive prompt
	// pre-checks every suggestion so accepting the form keeps the
	// templated form, but unattended runs leave compose env untouched.
	envAccepted := make([]bool, len(envSuggestions))
	if len(envSuggestions) > 0 {
		ambig = append(ambig, Ambiguity{
			Kind:    AmbEnvSuggestions,
			Message: fmt.Sprintf("%d env values look like cross-service URLs; review templatisation", len(envSuggestions)),
		})
	}

	return &Plan{
		Toplevel:       toplevel,
		ManifestPath:   manifestPath,
		Name:           name,
		Domain:         domain,
		ComposeFile:    composeFile,
		Candidates:     candidates,
		Selected:       selected,
		DefaultService: defaultService,
		WorktreeDir:    worktreeDir,
		BaseRef:        baseRef,
		Share:          !opts.Private,
		Existing:       existing,
		EnvSuggestions: envSuggestions,
		EnvAccepted:    envAccepted,
	}, ambig, nil
}

// AcceptedEnvSuggestions returns the suggestions the user opted into,
// preserving Plan order.
func (p *Plan) AcceptedEnvSuggestions() []EnvSuggestion {
	out := make([]EnvSuggestion, 0, len(p.EnvSuggestions))
	for i, s := range p.EnvSuggestions {
		if i < len(p.EnvAccepted) && p.EnvAccepted[i] {
			out = append(out, s)
		}
	}
	return out
}

// initialSelection pre-checks the multi-select. On a fresh init every
// candidate is selected; on re-init we mirror what the existing manifest
// already exposes so the wizard doesn't silently add or drop services.
//
// If the mirroring leaves nothing selected — typically because every
// previously-exposed service has been renamed or removed in the compose
// file — we fall back to "select all" so --yes still produces a valid
// manifest. The user can re-run interactively to pick a subset.
func initialSelection(candidates []ComposeCandidate, existing *manifest.Manifest) []bool {
	out := make([]bool, len(candidates))
	if existing == nil {
		for i := range out {
			out[i] = true
		}
		return out
	}
	exposed := map[string]bool{}
	for _, e := range existing.Expose {
		exposed[e.Service] = true
	}
	any := false
	for i, c := range candidates {
		out[i] = exposed[c.Service]
		any = any || out[i]
	}
	if !any {
		for i := range out {
			out[i] = true
		}
	}
	return out
}

func firstSelectedService(candidates []ComposeCandidate, selected []bool) string {
	for i, c := range candidates {
		if selected[i] {
			return c.Service
		}
	}
	return ""
}

func existingName(m *manifest.Manifest) string {
	if m == nil {
		return ""
	}
	return m.Project.Name
}

func existingDomain(m *manifest.Manifest) string {
	if m == nil {
		return ""
	}
	return m.Project.BaseDomain
}

func existingService(m *manifest.Manifest) string {
	if m == nil {
		return ""
	}
	return m.Stack.Service
}

func existingWorktreeDir(m *manifest.Manifest) string {
	if m == nil {
		return ""
	}
	return m.Worktree.Dir
}

func existingBaseRef(m *manifest.Manifest) string {
	if m == nil {
		return ""
	}
	return m.Worktree.BaseRef
}

// SelectedExposes materialises the [[expose]] rules from Selected.
//
// On re-init, host and port from the existing manifest win for services
// that survive: the user may have customised host="backend" or pinned a
// specific container port, and the wizard shouldn't clobber that.
func (p *Plan) SelectedExposes() []manifest.ExposeRule {
	prior := map[string]manifest.ExposeRule{}
	if p.Existing != nil {
		for _, e := range p.Existing.Expose {
			prior[e.Service] = e
		}
	}
	out := make([]manifest.ExposeRule, 0, len(p.Selected))
	for i, c := range p.Candidates {
		if !p.Selected[i] {
			continue
		}
		rule := manifest.ExposeRule{Service: c.Service, Port: c.Port}
		if e, ok := prior[c.Service]; ok {
			if e.Port > 0 {
				rule.Port = e.Port
			}
			rule.Host = e.Host
		}
		out = append(out, rule)
	}
	return out
}

// SelectedServiceNames returns just the service names that survived
// selection, in candidate order. Useful for huh.Select options.
func (p *Plan) SelectedServiceNames() []string {
	out := make([]string, 0, len(p.Selected))
	for i, c := range p.Candidates {
		if p.Selected[i] {
			out = append(out, c.Service)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
