package initwizard

import (
	"fmt"

	"github.com/charmbracelet/huh"
)

// PromptHuh resolves Plan ambiguities through a charm/huh form. The
// caller is expected to skip this when running unattended (--yes or no
// TTY); PromptHuh itself only short-circuits when no field needs input.
//
// We run up to two sequential forms instead of one big one because the
// "default service" choice depends on which services survived the
// expose multi-select, and huh groups don't recompute their options
// across runs cleanly.
func PromptHuh(p *Plan, ambig []Ambiguity) error {
	wantName, wantExpose, wantDefault := classify(ambig)

	if wantName || wantExpose {
		fields := []huh.Field{}
		if wantName {
			fields = append(fields, huh.NewInput().
				Title("Project name").
				Description("DNS label: lowercase, digits, dashes; must start and end with alphanumeric").
				Value(&p.Name).
				Validate(func(s string) error { return ValidateName(s) }))
		}

		var selected []string
		if wantExpose {
			opts := make([]huh.Option[string], 0, len(p.Candidates))
			for _, c := range p.Candidates {
				opts = append(opts, huh.NewOption(
					fmt.Sprintf("%s (container port %d)", c.Service, c.Port),
					c.Service,
				).Selected(true))
				selected = append(selected, c.Service)
			}
			fields = append(fields, huh.NewMultiSelect[string]().
				Title("Expose which services?").
				Description("Each gets a <service>.<slug>.<base_domain> URL").
				Options(opts...).
				Value(&selected).
				Validate(func(s []string) error {
					if len(s) == 0 {
						return fmt.Errorf("select at least one service")
					}
					return nil
				}))
		}

		if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
			return err
		}

		if wantExpose {
			applySelection(p, selected)
		}
	}

	// Default-service question depends on how many exposes survived.
	if wantDefault {
		names := p.SelectedServiceNames()
		if len(names) <= 1 {
			// Only one service exposed: it is the default by construction;
			// no prompt needed.
			if len(names) == 1 {
				p.DefaultService = names[0]
			}
			return nil
		}
		opts := make([]huh.Option[string], 0, len(names)+1)
		for _, n := range names {
			opts = append(opts, huh.NewOption(n, n))
		}
		opts = append(opts, huh.NewOption("(none — disable bare-slug alias)", ""))

		// Pre-select the current default if it survived selection,
		// otherwise the first exposed service.
		current := p.DefaultService
		if !contains(names, current) {
			current = names[0]
		}
		p.DefaultService = current

		sel := huh.NewSelect[string]().
			Title("Default service").
			Description("Gets the bare <slug>.<base_domain> alias").
			Options(opts...).
			Value(&p.DefaultService)

		if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
			return err
		}
	}

	return nil
}

func classify(ambig []Ambiguity) (name, expose, def bool) {
	for _, a := range ambig {
		switch a.Kind {
		case AmbInvalidName:
			name = true
		case AmbExpose:
			expose = true
		case AmbDefaultService:
			def = true
		}
	}
	return
}

// applySelection translates the chosen service names back into the
// boolean Selected slice that mirrors Candidates.
func applySelection(p *Plan, chosen []string) {
	picked := map[string]bool{}
	for _, n := range chosen {
		picked[n] = true
	}
	for i, c := range p.Candidates {
		p.Selected[i] = picked[c.Service]
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
