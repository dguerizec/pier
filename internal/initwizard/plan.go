package initwizard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	// Pre-filled with every candidate; PromptHuh lets the user toggle
	// individual ones off when there are multiple.
	Selected       []bool
	DefaultService string // name of the service for the bare-slug alias; "" disables the alias
	WorktreeDir    string
	BaseRef        string
	Share          bool
}

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
	if _, err := os.Stat(manifestPath); err == nil {
		return nil, nil, fmt.Errorf("%s already exists; remove it first or edit by hand", manifestPath)
	}

	composeFile, err := DetectComposeFile(toplevel, opts.File)
	if err != nil {
		return nil, nil, err
	}
	candidates := ListComposeServicesWithPorts(composeFile)
	if len(candidates) == 0 {
		return nil, nil, errors.New("no service with a published port detected; add `ports:` to at least one service in the compose file before running pier init")
	}

	var ambig []Ambiguity

	name := firstNonEmpty(opts.Name, Slugify(filepath.Base(toplevel)))
	if err := ValidateName(name); err != nil {
		ambig = append(ambig, Ambiguity{Kind: AmbInvalidName, Message: err.Error()})
	}

	domain := opts.Domain
	if domain == "" {
		// {pier.tld} expands at runtime so the same manifest works on
		// hosts that installed pier under different TLDs.
		domain = name + ".{pier.tld}"
	}

	selected := make([]bool, len(candidates))
	for i := range selected {
		selected[i] = true
	}
	if len(candidates) > 1 {
		ambig = append(ambig, Ambiguity{
			Kind:    AmbExpose,
			Message: fmt.Sprintf("%d services have published ports; pick which to expose", len(candidates)),
		})
	}

	defaultService := opts.Service
	if defaultService == "" {
		defaultService = candidates[0].Service
	}
	// Only flag the default-service question when we actually have a
	// meaningful choice (≥2 services AND the user didn't pin --service).
	if opts.Service == "" && len(candidates) > 1 {
		ambig = append(ambig, Ambiguity{
			Kind:    AmbDefaultService,
			Message: "which service gets the bare <slug>.<base_domain> alias?",
		})
	}

	worktreeDir := firstNonEmpty(opts.WorktreeDir, ".pier/worktrees")
	baseRef := firstNonEmpty(opts.BaseRef, DetectDefaultBranch(toplevel))

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
	}, ambig, nil
}

// SelectedExposes materialises the [[expose]] rules from Selected.
func (p *Plan) SelectedExposes() []manifest.ExposeRule {
	out := make([]manifest.ExposeRule, 0, len(p.Selected))
	for i, c := range p.Candidates {
		if !p.Selected[i] {
			continue
		}
		out = append(out, manifest.ExposeRule{Service: c.Service, Port: c.Port})
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
