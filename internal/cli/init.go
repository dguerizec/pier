package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/initwizard"
	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/worktree"
)

type initOpts struct {
	name        string
	domain      string
	service     string // designates the default exposed service for the bare-slug alias
	file        string
	private     bool
	yes         bool
	worktreeDir string
	baseRef     string
}

func newInitCmd() *cobra.Command {
	var opts initOpts
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Detect project type, generate .pier.toml manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := worktree.Detect()
			if err != nil {
				return err
			}
			if !info.IsPrimary {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: running pier init on a secondary worktree; the manifest will live there only")
			}
			return runInit(cmd.InOrStdin(), cmd.OutOrStdout(), info.Toplevel, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.name, "name", "", "project name (default: directory name)")
	f.StringVar(&opts.domain, "domain", "", "base domain (default: <name>.test)")
	f.StringVar(&opts.service, "service", "", "service that gets the bare <slug>.<base_domain> alias (default: first exposed)")
	f.StringVar(&opts.file, "file", "", "compose file path (default: auto-detect)")
	f.BoolVar(&opts.private, "private", false, "gitignore .pier.toml (default: commit it so secondary worktrees inherit it)")
	f.BoolVarP(&opts.yes, "yes", "y", false, "accept all defaults, no prompts")
	f.StringVar(&opts.worktreeDir, "worktree-dir", "", "where `pier worktree add <name>` places trees (default: .pier/worktrees)")
	f.StringVar(&opts.baseRef, "base-ref", "", "ref new worktree branches fork from (default: detected main/master)")
	return cmd
}

func runInit(stdin io.Reader, stdout io.Writer, toplevel string, opts initOpts) error {
	manifestPath := filepath.Join(toplevel, manifest.FileName)
	if _, err := os.Stat(manifestPath); err == nil {
		return fmt.Errorf("%s already exists; remove it first or edit by hand", manifestPath)
	}

	composeFile, err := initwizard.DetectComposeFile(toplevel, opts.file)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Detected: %s\n", filepath.Base(composeFile))

	candidates := initwizard.ListComposeServicesWithPorts(composeFile)
	switch len(candidates) {
	case 0:
		fmt.Fprintln(stdout, "  no service with published ports detected — pick one manually.")
	case 1:
		fmt.Fprintf(stdout, "  service: %s (container port %d)\n", candidates[0].Service, candidates[0].Port)
	default:
		fmt.Fprintln(stdout, "  services with published ports:")
		for _, c := range candidates {
			fmt.Fprintf(stdout, "    - %s (port %d)\n", c.Service, c.Port)
		}
	}

	reader := bufio.NewReader(stdin)
	defaultName := initwizard.Slugify(filepath.Base(toplevel))

	name := pick(opts.name, defaultName)
	if name == "" || !opts.yes {
		name = ask(reader, stdout, "Project name", name, opts.yes)
	}
	if err := initwizard.ValidateName(name); err != nil {
		return err
	}

	// Default base_domain uses the {pier.tld} template so the same manifest
	// stays portable across contributors who may run pier on different
	// TLDs. --domain forces an explicit literal when a project needs one.
	domain := opts.domain
	if domain == "" {
		domain = name + ".{pier.tld}"
	}

	if len(candidates) == 0 {
		return errors.New("no service with a published port detected; add `ports:` to at least one service in the compose file before running pier init")
	}

	exposes, err := pickExposes(reader, stdout, candidates, opts.yes)
	if err != nil {
		return err
	}

	defaultService := pick(opts.service, exposes[0].Service)
	if !opts.yes {
		defaultService = ask(reader, stdout,
			"Default service (gets bare <slug>.<base_domain> alias; blank to disable)",
			defaultService, false)
	}
	if defaultService != "" && !exposesContain(exposes, defaultService) {
		fmt.Fprintf(stdout, "warning: default service %q is not exposed; no alias will be emitted\n", defaultService)
	}

	// Default: manifest is committed so `git worktree add` carries it into
	// every new worktree. --private flips this off and gitignores the file.
	worktreeDir := pick(opts.worktreeDir, ".pier/worktrees")
	worktreeDir = ask(reader, stdout, "Worktree dir for `pier worktree add <name>` (blank to disable)", worktreeDir, opts.yes)

	baseRef := pick(opts.baseRef, initwizard.DetectDefaultBranch(toplevel))
	baseRef = ask(reader, stdout, "Base ref new branches fork from (blank to use git default)", baseRef, opts.yes)

	share := !opts.private
	if !opts.yes && !opts.private {
		share = askYesNo(reader, stdout, "Share manifest with team (commit to git)?", true)
	}

	m := &manifest.Manifest{
		Project: manifest.Project{Name: name, BaseDomain: domain},
		Stack: manifest.Stack{
			Kind:    manifest.KindCompose,
			File:    relTo(toplevel, composeFile),
			Service: defaultService,
		},
		Expose: exposes,
		Worktree: manifest.Worktree{
			Dir:     worktreeDir,
			BaseRef: baseRef,
		},
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if err := m.Write(manifestPath); err != nil {
		return err
	}

	if !share {
		if err := initwizard.EnsureGitignore(toplevel, manifest.FileName); err != nil {
			fmt.Fprintf(stdout, "warning: could not update .gitignore: %v\n", err)
		}
	}
	if err := initwizard.EnsureGitignore(toplevel, manifest.LocalFileName); err != nil {
		fmt.Fprintf(stdout, "warning: could not update .gitignore: %v\n", err)
	}
	if err := initwizard.EnsureGitignore(toplevel, ".pier/"); err != nil {
		fmt.Fprintf(stdout, "warning: could not update .gitignore: %v\n", err)
	}
	if entry := initwizard.WorktreeDirGitignoreEntry(toplevel, worktreeDir); entry != "" {
		if err := initwizard.EnsureGitignore(toplevel, entry); err != nil {
			fmt.Fprintf(stdout, "warning: could not update .gitignore: %v\n", err)
		}
	}

	fmt.Fprintf(stdout, "✓ %s written\n", manifestPath)
	return nil
}

// pickExposes walks the detected services and asks the user which ones to
// expose, with what host (sub-domain). With --yes (or no candidates that
// would conflict), it accepts every detected service with its name as host.
func pickExposes(reader *bufio.Reader, stdout io.Writer, candidates []initwizard.ComposeCandidate, yes bool) ([]manifest.ExposeRule, error) {
	var out []manifest.ExposeRule
	for _, c := range candidates {
		expose := true
		if !yes {
			expose = askYesNo(reader, stdout, fmt.Sprintf("Expose service %q on port %d?", c.Service, c.Port), true)
		}
		if !expose {
			continue
		}
		host := c.Service
		if !yes {
			host = ask(reader, stdout, fmt.Sprintf("  Host (sub-domain label) for %q", c.Service), host, false)
		}
		rule := manifest.ExposeRule{Service: c.Service, Port: c.Port}
		if host != c.Service {
			rule.Host = host
		}
		out = append(out, rule)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one service must be exposed")
	}
	return out, nil
}

func exposesContain(rules []manifest.ExposeRule, service string) bool {
	for _, r := range rules {
		if r.Service == service {
			return true
		}
	}
	return false
}

func ask(reader *bufio.Reader, out io.Writer, label, def string, yes bool) string {
	if yes {
		return def
	}
	if def != "" {
		fmt.Fprintf(out, "? %s [%s]: ", label, def)
	} else {
		fmt.Fprintf(out, "? %s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func askYesNo(reader *bufio.Reader, out io.Writer, label string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Fprintf(out, "? %s %s: ", label, hint)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	switch line {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

func pick(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func relTo(base, target string) string {
	if rel, err := filepath.Rel(base, target); err == nil {
		return rel
	}
	return target
}
