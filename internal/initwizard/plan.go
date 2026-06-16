package initwizard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/manifest"
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
	// MatchHostUID is a tri-state: nil means "fall back to existing
	// manifest, then the wizard default (true)"; non-nil pins the
	// value and suppresses the prompt. CLI exposes this through
	// --match-host-uid / --no-match-host-uid; only a Changed() flag
	// produces a non-nil value here.
	MatchHostUID *bool
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
	// MatchHostUID is the value Apply will write into [stack]. Defaults
	// to true on a fresh init (safe for distroless/nonroot images, no-op
	// for images that already run as root). Re-init inherits the
	// existing manifest's value.
	MatchHostUID bool

	// Existing is the previous manifest when re-running pier init on a
	// project that already has a .pier.toml. Apply uses it as the base for
	// the rewrite so user-curated sections (env, materialize, hooks, watch,
	// stack.match_host_uid) survive untouched. Nil on first init.
	Existing *manifest.Manifest

	// WorktreeDirExplicit is true when the user passed --worktree-dir on
	// the command line. Apply uses it to decide whether to persist the
	// value to ~/.config/pier/prefs.toml: implicit defaults stay
	// untouched so wizard runs don't keep churning the user's prefs.
	WorktreeDirExplicit bool

	// EnvSuggestions are templatisable env values discovered in the
	// compose file. Aligned with EnvAccepted: only suggestions whose flag
	// is true are written to the manifest.
	EnvSuggestions []EnvSuggestion
	EnvAccepted    []bool

	// EnvVarPrompts mirrors compose values that are pure host-side
	// interpolations (e.g. `${VITE_ALLOWED_HOSTS-}`). EnvVarValues holds
	// the per-prompt user input collected by PromptHuh; an empty value
	// means "skip — don't write this key into the manifest".
	EnvVarPrompts []EnvVarPrompt
	EnvVarValues  []string
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
	// AmbMatchHostUID is set when the user did not pin
	// --match-host-uid / --no-match-host-uid and we want to confirm the
	// default before writing it.
	AmbMatchHostUID
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
	// The MetaData captures which keys were physically present so we can
	// tell "user wrote false" from "key absent" — the latter falls back
	// to the wizard default rather than perpetuating a legacy zero value.
	var existing *manifest.Manifest
	var existingMeta toml.MetaData
	if _, err := os.Stat(manifestPath); err == nil {
		existing = &manifest.Manifest{}
		md, err := toml.DecodeFile(manifestPath, existing)
		if err != nil {
			return nil, nil, fmt.Errorf("re-init: parse %s: %w", manifestPath, err)
		}
		existingMeta = md
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

	worktreeDir := firstNonEmpty(
		opts.WorktreeDir,
		existingWorktreeDir(existing),
		loadPrefsWorktreeDir(),
		".pier/worktrees",
	)
	baseRef := firstNonEmpty(opts.BaseRef, existingBaseRef(existing), DetectDefaultBranch(toplevel))

	envSuggestions := ScanEnvSuggestions(composeFile, existing)
	// Suggestions are off by default: rewriting a service's env behind the
	// user's back is too sharp an edge for --yes. The interactive prompt
	// pre-checks every suggestion so accepting the form keeps the
	// templated form, but unattended runs leave compose env untouched.
	envAccepted := make([]bool, len(envSuggestions))

	envVarPrompts := ScanEnvVarPrompts(composeFile, existing)
	// Values start empty so --yes / non-TTY doesn't accidentally pin the
	// compose default into the manifest. PromptHuh copies Default into
	// EnvVarValues just before rendering the input so the user sees the
	// upstream default but explicitly chooses to keep, change, or skip.
	envVarValues := make([]string, len(envVarPrompts))

	if len(envSuggestions) > 0 || len(envVarPrompts) > 0 {
		ambig = append(ambig, Ambiguity{
			Kind: AmbEnvSuggestions,
			Message: fmt.Sprintf("%d cross-service URLs and %d host-interpolated values found in compose env",
				len(envSuggestions), len(envVarPrompts)),
		})
	}

	// match_host_uid resolution: explicit flag wins, then the existing
	// manifest's value when the key was actually present, then the safe
	// default (true). Legacy manifests written before the key existed
	// parse to false (Go zero value), which would silently flip the
	// prompt default to false on re-init — IsDefined distinguishes
	// "user wrote false" from "key absent".
	matchHostUID := true
	if opts.MatchHostUID != nil {
		matchHostUID = *opts.MatchHostUID
	} else if existing != nil && existingMeta.IsDefined("stack", "match_host_uid") {
		matchHostUID = existing.Stack.MatchHostUID
	}
	if opts.MatchHostUID == nil {
		ambig = append(ambig, Ambiguity{
			Kind:    AmbMatchHostUID,
			Message: "run containers as your host UID:GID (avoids root-owned files in bind mounts)?",
		})
	}

	return &Plan{
		Toplevel:            toplevel,
		ManifestPath:        manifestPath,
		Name:                name,
		Domain:              domain,
		ComposeFile:         composeFile,
		Candidates:          candidates,
		Selected:            selected,
		DefaultService:      defaultService,
		WorktreeDir:         worktreeDir,
		BaseRef:             baseRef,
		Share:               !opts.Private,
		MatchHostUID:        matchHostUID,
		Existing:            existing,
		WorktreeDirExplicit: opts.WorktreeDir != "",
		EnvSuggestions:      envSuggestions,
		EnvAccepted:         envAccepted,
		EnvVarPrompts:       envVarPrompts,
		EnvVarValues:        envVarValues,
	}, ambig, nil
}

// loadPrefsWorktreeDir reads the per-user worktree default from
// prefs.toml. Errors and missing files collapse to the empty string so
// the caller can fall through to the next source in the resolution
// chain.
func loadPrefsWorktreeDir() string {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return ""
	}
	prefs, err := infra.LoadPrefs(paths)
	if err != nil {
		return ""
	}
	return prefs.WorktreeDir
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

// FilledEnvVarPrompts returns the prompt/value pairs that ended up with
// a non-empty value. Apply uses these to populate [env.<service>].
func (p *Plan) FilledEnvVarPrompts() []EnvVarPrompt {
	out := make([]EnvVarPrompt, 0, len(p.EnvVarPrompts))
	for i, prompt := range p.EnvVarPrompts {
		if i >= len(p.EnvVarValues) {
			break
		}
		if strings.TrimSpace(p.EnvVarValues[i]) == "" {
			continue
		}
		out = append(out, prompt)
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
// On re-init, host, port, and preserve_ports from the existing manifest win
// for services that survive: the user may have customised host="backend",
// pinned a specific container port, or kept TCP host bindings, and the wizard
// shouldn't clobber that.
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
			rule.PreservePorts = e.PreservePorts
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
