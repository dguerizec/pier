package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/cli/skill"
	"github.com/LeoPartt/pier/internal/detect"
	"github.com/LeoPartt/pier/internal/headscale"
	"github.com/LeoPartt/pier/internal/infra"
)

type installOpts struct {
	mode                      string
	tld                       string
	manualDNS                 bool
	noSudo                    bool
	bindIP                    string
	answerIP                  string
	externalTraefik           string
	traefikNetwork            string
	externalTraefikDynamicDir string
	yes                       bool
}

func newInstallCmd() *cobra.Command {
	var opts installOpts
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Bootstrap traefik + dnsmasq + host DNS (run once per machine)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.noSudo {
				opts.manualDNS = true
			}
			// Wizard mode: no positional opts touched → detect environment
			// and compose a plan, confirm, then call Install with the
			// derived flags.
			if shouldRunWizard(cmd) {
				return runInstallWizard(cmd, opts)
			}
			mode := opts.mode
			if mode == "" {
				mode = infra.ModeLocal
			}
			if err := infra.Install(infra.InstallOptions{
				Mode:                      mode,
				TLD:                       opts.tld,
				BindIP:                    opts.bindIP,
				AnswerIP:                  opts.answerIP,
				ManualDNS:                 opts.manualDNS,
				Out:                       cmd.OutOrStdout(),
				ExternalTraefik:           opts.externalTraefik,
				TraefikNetwork:            opts.traefikNetwork,
				ExternalTraefikDynamicDir: opts.externalTraefikDynamicDir,
			}); err != nil {
				return err
			}
			installUserSkill(cmd.OutOrStdout())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.mode, "mode", "", "local | server (only local supported in MVP)")
	f.StringVar(&opts.tld, "tld", infra.DefaultTLD, "base TLD (RFC2606 reserved recommended)")
	f.BoolVar(&opts.manualDNS, "manual-dns", false, "skip system DNS modification, print instructions instead")
	f.BoolVar(&opts.noSudo, "no-sudo", false, "alias of --manual-dns")
	f.StringVar(&opts.bindIP, "bind-ip", "", "traefik/dnsmasq listen IP (default: 127.0.0.1 local, 0.0.0.0 server)")
	f.StringVar(&opts.answerIP, "answer-ip", "", "IP dnsmasq returns for *.<tld> (server mode; auto-detected from tailscale when omitted)")
	f.StringVar(&opts.externalTraefik, "use-existing-traefik", "", "BYO mode: name of an existing traefik container to register workloads on")
	f.StringVar(&opts.traefikNetwork, "traefik-network", "", "BYO mode: docker network for label discovery (auto-detected from the existing traefik when omitted)")
	f.StringVar(&opts.externalTraefikDynamicDir, "traefik-dynamic-dir", "", "BYO mode: host path of the existing traefik's file-provider directory (auto-detected when omitted; required to expose http://pier.<tld>)")
	f.BoolVarP(&opts.yes, "yes", "y", false, "accept the detected wizard plan without prompting")
	return cmd
}

// shouldRunWizard reports whether to enter the auto-detect wizard. We do so
// when none of the install-shape flags are present — implying the user
// expects pier to figure things out — and explicit configuration takes
// precedence whenever any of those flags is set.
func shouldRunWizard(cmd *cobra.Command) bool {
	for _, name := range []string{"mode", "bind-ip", "answer-ip", "use-existing-traefik", "traefik-network", "traefik-dynamic-dir"} {
		if cmd.Flags().Changed(name) {
			return false
		}
	}
	return true
}

// runInstallWizard inspects the host, prints a single suggested plan, and
// applies it on confirmation.
func runInstallWizard(cmd *cobra.Command, base installOpts) error {
	out := cmd.OutOrStdout()
	env := detect.Run()

	fmt.Fprintln(out, "Detected:")
	for _, line := range env.Summary() {
		fmt.Fprintln(out, "  "+line)
	}
	fmt.Fprintln(out)

	// In wizard mode the cobra flag default for --tld ("test") shouldn't
	// drown out detection; treat unchanged --tld as "no opinion" so we can
	// suggest pier.<base_domain> when headscale is around.
	if !cmd.Flags().Changed("tld") {
		base.tld = ""
	}

	plan := composeInstallPlan(env, base)

	// Host-process traefik can't drive pier workloads (no docker
	// labels) and will fight pier-traefik for port 80. Surface the
	// conflict before we plan a port-80 spawn.
	if env.Traefik.Found && env.Traefik.Container == "" {
		fmt.Fprintf(out, "! detected a host-process traefik")
		if env.Traefik.DynamicDir != "" {
			fmt.Fprintf(out, " (dynamic_dir=%s)", env.Traefik.DynamicDir)
		}
		fmt.Fprintln(out, ".")
		fmt.Fprintln(out, "  pier-traefik will conflict on port 80. Stop the host traefik first,")
		fmt.Fprintln(out, "  or move it into docker and re-run `pier install` for auto-BYO.")
		fmt.Fprintln(out)
	}

	// In BYO mode pier serve drops pier-dashboard.yml in the user's
	// file-provider dir so http://pier.<tld> resolves through their
	// traefik. If detection couldn't pin it down, ask before applying
	// — better one extra prompt than a silent no-op at runtime.
	if plan.ExternalTraefik != "" && plan.ExternalTraefikDynamicDir == "" && !base.yes {
		plan.ExternalTraefikDynamicDir = promptTraefikDynamicDir(cmd, plan.ExternalTraefik)
	}

	fmt.Fprintln(out, "Plan:")
	fmt.Fprintln(out, "  "+planSummary(plan))
	fmt.Fprintln(out)

	if !base.yes {
		if !confirm(cmd.InOrStdin(), out, "Apply this plan?", true) {
			fmt.Fprintln(out, "(aborted — pass explicit flags to override the detected defaults)")
			return nil
		}
	}

	if err := infra.Install(plan); err != nil {
		return err
	}

	installUserSkill(out)
	askWorktreeDirPref(cmd, base.yes)

	if env.Headscale.Found && env.Tailscale.Active && env.Headscale.ConfigPath != "" {
		// Records mode is already wired through Config — no headscale
		// auto-patch needed. The MagicDNS layer resolves slugs as soon as
		// pier up writes them.
		if plan.HeadscaleRecordsPath != "" {
			fmt.Fprintln(out)
			fmt.Fprintf(out, "✓ records mode: pier up/down will manage %s\n", plan.HeadscaleRecordsPath)
			return nil
		}
		fmt.Fprintln(out)
		// Split-DNS push only works for TLDs OUTSIDE the headscale
		// base_domain. tldIsUnder false ⇒ safe to auto-patch.
		if env.Headscale.BaseDomain != "" && tldIsUnder(plan.TLD, env.Headscale.BaseDomain) {
			fmt.Fprintf(out, "! pier TLD %q is under headscale base_domain %q but extra_records is not configured.\n", plan.TLD, env.Headscale.BaseDomain)
			fmt.Fprintln(out, "  Pick a TLD outside the base_domain (e.g. `--tld test`) for split-DNS,")
			fmt.Fprintln(out, "  or set dns.extra_records_path in headscale.yaml so pier can publish records.")
			return nil
		}
		if base.yes || confirm(cmd.InOrStdin(), out, fmt.Sprintf("Patch %s with .%s split-DNS?", env.Headscale.ConfigPath, plan.TLD), true) {
			changed, err := headscale.Patch(env.Headscale.ConfigPath, plan.TLD, plan.AnswerIP)
			if err != nil {
				fmt.Fprintf(out, "! headscale patch failed (%v) — fall back to `pier client tailscale` for the manual snippet\n", err)
				return nil
			}
			if !changed {
				fmt.Fprintln(out, "✓ headscale already configured for this TLD (no-op)")
				return nil
			}
			fmt.Fprintf(out, "✓ patched %s (backup at %s.bak)\n", env.Headscale.ConfigPath, env.Headscale.ConfigPath)
			if err := headscale.Reload(env.Headscale.Container); err != nil {
				fmt.Fprintf(out, "! reload headscale failed (%v) — restart the container manually: docker restart %s\n", err, env.Headscale.Container)
			} else {
				fmt.Fprintln(out, "✓ headscale restarted (DNS config reload)")
				fmt.Fprintln(out, "  note: peers test the rule with `resolvectl query <name>.<tld>`; `dig` doesn't")
				fmt.Fprintln(out, "        always honour systemd-resolved per-link routing and will look broken.")
			}
		}
	}
	return nil
}

// installUserSkill drops the embedded skill tree under
// ~/.claude/skills/pier/. Best-effort: a failure here doesn't block the
// install — the user may not run Claude Code, or may be on a machine
// where $HOME isn't writable. pier install is idempotent; we always
// overwrite to keep the skill in sync with the installed binary version.
func installUserSkill(out io.Writer) {
	dir, err := skill.UserDir()
	if err != nil {
		fmt.Fprintf(out, "! skill: %v (skipped)\n", err)
		return
	}
	if err := skill.Install(dir); err != nil {
		fmt.Fprintf(out, "! skill install failed: %v (skipped)\n", err)
		return
	}
	fmt.Fprintf(out, "✓ AI skill installed: %s\n", dir)
}

// tldIsUnder reports whether tld is the same as base or a sub-domain of it.
// Used to refuse auto-patching headscale for TLDs that fall under MagicDNS's
// authoritative scope.
func tldIsUnder(tld, base string) bool {
	return tld == base || strings.HasSuffix(tld, "."+base)
}

// composeInstallPlan turns detected environment + user flags into the
// concrete InstallOptions. Explicit flags always win over detected values.
func composeInstallPlan(env detect.Environment, base installOpts) infra.InstallOptions {
	plan := infra.InstallOptions{
		Mode:                      base.mode,
		TLD:                       base.tld,
		BindIP:                    base.bindIP,
		AnswerIP:                  base.answerIP,
		ManualDNS:                 base.manualDNS,
		ExternalTraefik:           base.externalTraefik,
		TraefikNetwork:            base.traefikNetwork,
		ExternalTraefikDynamicDir: base.externalTraefikDynamicDir,
	}
	if plan.TLD == "" {
		// When extra_records is available, pier can publish per-slug records
		// directly under the headscale base_domain (MagicDNS resolves them
		// for free) — that's strictly better than inventing a `.test` TLD
		// alongside. Otherwise default to .test (split-DNS path).
		if env.Headscale.RecordsPath != "" && env.Headscale.BaseDomain != "" {
			plan.TLD = env.Headscale.BaseDomain
		} else {
			plan.TLD = infra.DefaultTLD
		}
	}
	switch {
	case env.Tailscale.Active:
		if plan.Mode == "" {
			plan.Mode = infra.ModeServer
		}
		if plan.BindIP == "" {
			plan.BindIP = env.Tailscale.IPv4
		}
		if plan.AnswerIP == "" {
			plan.AnswerIP = env.Tailscale.IPv4
		}
	default:
		if plan.Mode == "" {
			plan.Mode = infra.ModeLocal
		}
	}
	if env.Traefik.Found && env.Traefik.Container != "" {
		// Only auto-trigger BYO when we found a docker container — pier
		// registers workloads with docker labels, so a host-process
		// traefik can't drive them. When that's the case the wizard
		// surfaces a warning but otherwise leaves the install alone.
		if plan.ExternalTraefik == "" {
			plan.ExternalTraefik = env.Traefik.Container
		}
		if plan.TraefikNetwork == "" {
			plan.TraefikNetwork = env.Traefik.Network
		}
		if plan.ExternalTraefikDynamicDir == "" {
			plan.ExternalTraefikDynamicDir = env.Traefik.DynamicDir
		}
	}
	// Records mode kicks in only when the chosen TLD is a sub-domain of (or
	// identical to) the headscale base_domain — that's the case where
	// MagicDNS owns the lookups and split-DNS is preempted. In other
	// cases the records publication wouldn't survive a peer query.
	if env.Headscale.RecordsPath != "" && env.Headscale.BaseDomain != "" && tldIsUnder(plan.TLD, env.Headscale.BaseDomain) {
		plan.HeadscaleContainer = env.Headscale.Container
		plan.HeadscaleRecordsPath = env.Headscale.RecordsPath
	}
	return plan
}

func planSummary(p infra.InstallOptions) string {
	parts := []string{"--mode " + p.Mode, "--tld " + p.TLD}
	if p.BindIP != "" {
		parts = append(parts, "--bind-ip "+p.BindIP)
	}
	if p.AnswerIP != "" && p.AnswerIP != p.BindIP {
		parts = append(parts, "--answer-ip "+p.AnswerIP)
	}
	if p.ExternalTraefik != "" {
		parts = append(parts, "--use-existing-traefik "+p.ExternalTraefik)
	}
	if p.TraefikNetwork != "" {
		parts = append(parts, "--traefik-network "+p.TraefikNetwork)
	}
	if p.ExternalTraefikDynamicDir != "" {
		parts = append(parts, "--traefik-dynamic-dir "+p.ExternalTraefikDynamicDir)
	}
	if p.HeadscaleRecordsPath != "" {
		parts = append(parts, "(records mode: "+p.HeadscaleRecordsPath+")")
	}
	return strings.Join(parts, " ")
}

// askWorktreeDirPref prompts once during the install wizard for the
// per-user default worktree dir and saves it to prefs.toml. Called at
// the tail of the wizard so the user has done all the infra steps and
// reads this as a workflow preference, not an install option.
//
// Skips silently when:
//   - --yes is in effect (script-style install must not block on UX),
//   - prefs.toml already has a worktree_dir (re-running install
//     shouldn't keep re-asking).
func askWorktreeDirPref(cmd *cobra.Command, yes bool) {
	if yes {
		return
	}
	paths, err := infra.DefaultPaths()
	if err != nil {
		return
	}
	prefs, err := infra.LoadPrefs(paths)
	if err != nil {
		return
	}
	if prefs.WorktreeDir != "" {
		return
	}

	out := cmd.OutOrStdout()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Where should `pier worktree add <name>` place new worktrees by default?")
	fmt.Fprintln(out, "  Examples: .pier/worktrees (in-repo), ~/wt/<project> (in $HOME), /srv/wt (absolute)")
	fmt.Fprintln(out, "  Each project can still pin its own location via [worktree].dir in .pier.toml.")
	fmt.Fprint(out, "Worktree dir [skip]: ")

	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		fmt.Fprintln(out, "  (skipped — built-in default `.pier/worktrees` applies)")
		return
	}
	prefs.WorktreeDir = line
	if err := prefs.Save(paths); err != nil {
		fmt.Fprintf(out, "! could not save prefs: %v\n", err)
		return
	}
	fmt.Fprintf(out, "✓ saved worktree dir to %s\n", infra.PrefsPath(paths))
}

// promptTraefikDynamicDir asks the user to type the host path of
// their traefik's file-provider directory. Used in BYO mode when
// detection couldn't extract the path from the container's argv or
// static config.
//
// Validates that the path exists and is a writable directory before
// returning. Re-prompts on typos so a stale path doesn't persist
// silently into config.toml only to fail at `pier serve` runtime.
// Empty input means "skip" — pier serve no-ops the dashboard route
// registration with no surprise.
func promptTraefikDynamicDir(cmd *cobra.Command, container string) string {
	out := cmd.OutOrStdout()
	in := bufio.NewReader(cmd.InOrStdin())
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Could not auto-detect the file-provider directory of traefik container %q.\n", container)
	fmt.Fprintln(out, "  Without it pier serve cannot expose http://pier.<tld>; everything else still works.")
	for {
		fmt.Fprint(out, "Path to the traefik dynamic dir [skip]: ")
		line, _ := in.ReadString('\n')
		path := strings.TrimSpace(line)
		if path == "" {
			return ""
		}
		if err := validateTraefikDynamicDir(path); err != nil {
			fmt.Fprintf(out, "  %v — try again, or hit enter to skip.\n", err)
			continue
		}
		return path
	}
}

// validateTraefikDynamicDir checks that path is an existing directory
// pier serve will be able to drop pier-dashboard.yml into. We don't
// actually probe write access (a touch/remove would race with
// concurrent pier servers); a directory + sane mode is enough at
// install time.
func validateTraefikDynamicDir(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// confirm reads a yes/no answer from stdin. Default applies on empty input.
func confirm(stdin interface{ Read([]byte) (int, error) }, out interface{ Write([]byte) (int, error) }, prompt string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Fprintf(out, "%s %s ", prompt, hint)
	r := bufio.NewReader(stdin)
	line, _ := r.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

func newUninstallCmd() *cobra.Command {
	var (
		manualDNS bool
		purge     bool
	)
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop infra containers, remove resolver files, clear config dir",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if err := infra.Uninstall(out, manualDNS); err != nil {
				return err
			}
			if dir, err := skill.UserDir(); err == nil {
				if removed, err := skill.Uninstall(dir); err != nil {
					fmt.Fprintf(out, "! skill removal failed: %v\n", err)
				} else if removed {
					fmt.Fprintf(out, "✓ AI skill removed: %s\n", dir)
				}
			}
			if purge {
				if err := purgeBinary(out); err != nil {
					fmt.Fprintf(out, "! remove binary: %v\n", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&manualDNS, "manual-dns", false, "do not touch host DNS, print revert instructions instead")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove the pier binary itself (skipped when installed by a package manager)")
	return cmd
}

// purgeBinary deletes the pier executable that's running this very call.
// Linux and macOS keep the running inode alive until the process exits, so
// unlinking the file we're executing from is safe — the kernel reaps it
// once the command returns.
//
// Refuses when the binary lives under a path commonly owned by a package
// manager (brew prefix, system /usr) so we don't leave stale metadata in
// the manager's database. The expected install paths from install.sh
// (~/.local/bin, /usr/local/bin) and the from-source Makefile target both
// fall outside that list and are removed normally.
func purgeBinary(out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if mgr := managedBy(resolved); mgr != "" {
		fmt.Fprintf(out, "! skipping binary removal: %s looks managed by %s\n", resolved, mgr)
		fmt.Fprintf(out, "  use the package manager to uninstall (e.g. `brew uninstall pier`)\n")
		return nil
	}
	if err := os.Remove(resolved); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ removed binary %s\n", resolved)
	return nil
}

// managedBy returns a short label of the package manager that likely owns
// path, or "" when pier looks free-standing. Conservative on purpose: when
// in doubt, return "" so --purge removes the file. The skip exists mainly
// to protect brew/apt metadata.
func managedBy(path string) string {
	switch {
	case strings.HasPrefix(path, "/opt/homebrew/"),
		strings.HasPrefix(path, "/usr/local/Cellar/"),
		strings.HasPrefix(path, "/home/linuxbrew/.linuxbrew/"):
		return "homebrew"
	case strings.HasPrefix(path, "/usr/bin/"),
		strings.HasPrefix(path, "/usr/sbin/"),
		strings.HasPrefix(path, "/bin/"),
		strings.HasPrefix(path, "/sbin/"):
		return "the system package manager"
	}
	return ""
}
