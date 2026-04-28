package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"gopkg.in/yaml.v3"

	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/worktree"
)

// yamlUnmarshal is a thin alias to keep the imports tidy at use sites.
func yamlUnmarshal(in []byte, out any) error { return yaml.Unmarshal(in, out) }

// installedTLD returns the TLD pier was installed with so init's default
// base_domain is coherent with the host (e.g. `<name>.nebula` when pier
// runs in records mode under base_domain=nebula). Falls back to the
// hard-coded `.test` when pier isn't installed yet — pier init shouldn't
// require pier install.
func installedTLD() string {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return infra.DefaultTLD
	}
	cfg, err := infra.LoadConfig(paths)
	if err != nil || cfg.TLD == "" {
		return infra.DefaultTLD
	}
	return cfg.TLD
}

type initOpts struct {
	name        string
	domain      string
	service     string
	port        int
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
	f.StringVar(&opts.service, "service", "", "compose service name")
	f.IntVar(&opts.port, "port", 0, "service port exposed by the workload")
	f.StringVar(&opts.file, "file", "", "compose file path (default: auto-detect)")
	f.BoolVar(&opts.private, "private", false, "gitignore .pier.toml (default: commit it so secondary worktrees inherit it)")
	f.BoolVarP(&opts.yes, "yes", "y", false, "accept all defaults, no prompts")
	f.StringVar(&opts.worktreeDir, "worktree-dir", "", "where `pier worktree add <name>` places trees (default: .claude/worktrees)")
	f.StringVar(&opts.baseRef, "base-ref", "", "ref new worktree branches fork from (default: detected main/master)")
	return cmd
}

func runInit(stdin io.Reader, stdout io.Writer, toplevel string, opts initOpts) error {
	manifestPath := filepath.Join(toplevel, manifest.FileName)
	if _, err := os.Stat(manifestPath); err == nil {
		return fmt.Errorf("%s already exists; remove it first or edit by hand", manifestPath)
	}

	composeFile, err := detectComposeFile(toplevel, opts.file)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Detected: %s\n", filepath.Base(composeFile))

	autoService, autoPort := guessComposeServiceAndPort(composeFile)
	if autoService != "" {
		fmt.Fprintf(stdout, "  service: %s%s\n", autoService, portSuffix(autoPort))
	}

	reader := bufio.NewReader(stdin)
	defaultName := slugify(filepath.Base(toplevel))

	name := pick(opts.name, defaultName)
	if name == "" || !opts.yes {
		name = ask(reader, stdout, "Project name", name, opts.yes)
	}
	if err := validateName(name); err != nil {
		return err
	}

	defaultDomain := name + "." + installedTLD()
	domain := pick(opts.domain, defaultDomain)
	if domain == "" || !opts.yes {
		domain = ask(reader, stdout, "Base domain", domain, opts.yes)
	}

	service := pick(opts.service, autoService)
	if service == "" || !opts.yes {
		service = ask(reader, stdout, "Service to expose (compose service name)", service, opts.yes)
	}
	if service == "" {
		return errors.New("service to expose is required (use --service or answer the prompt)")
	}

	portStr := ""
	if opts.port != 0 {
		portStr = strconv.Itoa(opts.port)
	} else if autoPort != 0 {
		portStr = strconv.Itoa(autoPort)
	}
	portStr = ask(reader, stdout, "Container port", portStr, opts.yes)
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return fmt.Errorf("invalid port %q", portStr)
	}

	// Default: manifest is committed so `git worktree add` carries it into
	// every new worktree. --private flips this off and gitignores the file.
	worktreeDir := pick(opts.worktreeDir, ".claude/worktrees")
	worktreeDir = ask(reader, stdout, "Worktree dir for `pier worktree add <name>` (blank to disable)", worktreeDir, opts.yes)

	baseRef := pick(opts.baseRef, detectDefaultBranch(toplevel))
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
			Service: service,
			Port:    port,
		},
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
		if err := ensureGitignore(toplevel, manifest.FileName); err != nil {
			fmt.Fprintf(stdout, "warning: could not update .gitignore: %v\n", err)
		}
	}
	if err := ensureGitignore(toplevel, manifest.LocalFileName); err != nil {
		fmt.Fprintf(stdout, "warning: could not update .gitignore: %v\n", err)
	}
	if err := ensureGitignore(toplevel, ".pier/"); err != nil {
		fmt.Fprintf(stdout, "warning: could not update .gitignore: %v\n", err)
	}

	fmt.Fprintf(stdout, "✓ %s written\n", manifestPath)
	return nil
}

// guessComposeServiceAndPort parses path and returns the only service that
// declares a published port, plus the container-side port. Returns empty
// strings when there's no unambiguous candidate (zero, two+, no ports).
//
// We don't pull compose-go for this — a tiny stub of the relevant fields is
// enough and keeps the dep graph small.
func guessComposeServiceAndPort(path string) (service string, port int) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	var doc struct {
		Services map[string]struct {
			Ports []any `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yamlUnmarshal(body, &doc); err != nil {
		return "", 0
	}
	candidates := make([]string, 0, len(doc.Services))
	ports := make(map[string]int, len(doc.Services))
	for name, svc := range doc.Services {
		for _, p := range svc.Ports {
			if cp := parseContainerPort(p); cp > 0 {
				candidates = append(candidates, name)
				ports[name] = cp
				break
			}
		}
	}
	if len(candidates) == 1 {
		return candidates[0], ports[candidates[0]]
	}
	return "", 0
}

// parseContainerPort extracts the container-side port from a compose ports
// entry. Handles short syntax ("3000", "8080:3000", "${PORT:-8080}:3000")
// and the long form ({"target": 3000, "published": 8080}).
func parseContainerPort(entry any) int {
	switch v := entry.(type) {
	case string:
		// Container port is the right-most colon-separated segment, after
		// stripping any /protocol suffix.
		s := v
		if idx := strings.LastIndex(s, ":"); idx >= 0 {
			s = s[idx+1:]
		}
		if idx := strings.Index(s, "/"); idx >= 0 {
			s = s[:idx]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	case int:
		return v
	case map[string]any:
		if t, ok := v["target"]; ok {
			switch tv := t.(type) {
			case int:
				return tv
			case string:
				if n, err := strconv.Atoi(tv); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func portSuffix(p int) string {
	if p == 0 {
		return ""
	}
	return fmt.Sprintf(" (container port %d)", p)
}

// detectDefaultBranch returns the conventional default branch (main or
// master) of the repo at toplevel, or "" when neither exists.
func detectDefaultBranch(toplevel string) string {
	for _, candidate := range []string{"main", "master"} {
		c := exec.Command("git", "rev-parse", "--verify", "--quiet", candidate)
		c.Dir = toplevel
		if c.Run() == nil {
			return candidate
		}
	}
	return ""
}

// detectComposeFile resolves the compose file to use. Order matches DESIGN §3.2.
func detectComposeFile(toplevel, override string) (string, error) {
	if override != "" {
		path := override
		if !filepath.IsAbs(path) {
			path = filepath.Join(toplevel, path)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("compose file %s not found", path)
		}
		return path, nil
	}
	candidates := []string{
		"docker-compose.dev.yml",
		"docker-compose.dev.yaml",
		"compose.dev.yml",
		"compose.dev.yaml",
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}
	for _, name := range candidates {
		p := filepath.Join(toplevel, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New(`no docker-compose file found in this directory.

pier is intentionally coupled to docker compose: every workload — even raw
processes (uv/npm/cargo) — runs inside a container so worktrees stay
isolated and reproducible across hosts.

If your project doesn't otherwise containerize, drop a minimal
docker-compose.dev.yml at its root, e.g.:

  services:
    app:
      image: python:3.13-slim          # or node:20, rust:1, ...
      working_dir: /app
      volumes:
        - ./:/app
      command: sh -c "pip install uv && uv sync && uv run python run.py"
      ports:
        - "${PORT:-3000}:3000"

Then re-run pier init.`)
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

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	out := slugRE.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(out, "-")
}

func validateName(name string) error {
	dnsLabel := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	if !dnsLabel.MatchString(name) {
		return fmt.Errorf("project name %q is not a valid DNS label", name)
	}
	return nil
}

// ensureGitignore appends entry to <toplevel>/.gitignore if not already there.
func ensureGitignore(toplevel, entry string) error {
	path := filepath.Join(toplevel, ".gitignore")
	body, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(body) > 0 && !strings.HasSuffix(string(body), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(f, entry)
	return err
}
