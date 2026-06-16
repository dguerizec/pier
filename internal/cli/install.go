package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/cli/skill"
	"github.com/dguerizec/pier/internal/detect"
	"github.com/dguerizec/pier/internal/headscale"
	"github.com/dguerizec/pier/internal/infra"
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
			installUserSkill(cmd.InOrStdin(), cmd.OutOrStdout(), opts.yes)
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

	// Refuse to install when the chosen TLD lives under headscale's
	// base_domain. MagicDNS owns the lookups authoritatively for that
	// scope, so the split-DNS rule pier would push reaches peers as a
	// search domain only — workloads never resolve. Catch it BEFORE
	// spawning containers and writing host DNS, otherwise the user
	// ends up with a half-applied install pointing at a TLD that
	// can't work.
	if env.Headscale.Found && env.Headscale.BaseDomain != "" && tldIsUnder(plan.TLD, env.Headscale.BaseDomain) {
		fmt.Fprintf(out, "! pier TLD %q lives under headscale base_domain %q.\n", plan.TLD, env.Headscale.BaseDomain)
		fmt.Fprintln(out, "  MagicDNS pre-empts split-DNS for names under base_domain — workloads won't resolve from peers.")
		fmt.Fprintln(out, "  Pick a TLD outside the base_domain (e.g. `--tld test`) and re-run install.")
		return nil
	}

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

	installUserSkill(cmd.InOrStdin(), out, base.yes)
	askWorktreeDirPref(cmd, base.yes)

	if env.Headscale.Found && env.Tailscale.Active && env.Headscale.ConfigPath != "" {
		fmt.Fprintln(out)
		// Pre-flight already refused TLD-under-base_domain, so anything
		// reaching here is safe to auto-patch.
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
			}
		}
	}
	return nil
}

// installUserSkill drops the embedded skill tree under ~/.agents/skills/pier/
// and links detected agent-specific skill dirs to it. Best-effort: a failure
// here doesn't block the infra install, but conflicts are never overwritten
// without an explicit interactive confirmation.
func installUserSkill(stdin interface{ Read([]byte) (int, error) }, out io.Writer, yes bool) {
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

	targets, err := skill.DetectedLinkTargets()
	if err != nil {
		fmt.Fprintf(out, "! skill links: %v (skipped)\n", err)
		return
	}
	for _, target := range targets {
		status, err := skill.LinkState(target.Dir, dir)
		if err != nil {
			fmt.Fprintf(out, "! %s skill link check failed: %v\n", target.Agent, err)
			continue
		}
		switch status {
		case skill.LinkCurrent:
			fmt.Fprintf(out, "✓ %s skill already linked: %s\n", target.Agent, target.Dir)
		case skill.LinkMissing:
			if err := skill.Link(target.Dir, dir); err != nil {
				fmt.Fprintf(out, "! %s skill link failed: %v\n", target.Agent, err)
				continue
			}
			fmt.Fprintf(out, "✓ %s skill linked: %s → %s\n", target.Agent, target.Dir, dir)
		case skill.LinkConflict:
			if yes {
				fmt.Fprintf(out, "! %s skill exists and is not a pier symlink: %s (skipped)\n", target.Agent, target.Dir)
				continue
			}
			prompt := fmt.Sprintf("Replace existing %s skill at %s with a symlink to %s?", target.Agent, target.Dir, dir)
			if !confirm(stdin, out, prompt, false) {
				fmt.Fprintf(out, "! %s skill link skipped: %s\n", target.Agent, target.Dir)
				continue
			}
			if err := skill.Link(target.Dir, dir); err != nil {
				fmt.Fprintf(out, "! %s skill link failed: %v\n", target.Agent, err)
				continue
			}
			fmt.Fprintf(out, "✓ %s skill linked: %s → %s\n", target.Agent, target.Dir, dir)
		}
	}
}

// tldIsUnder reports whether tld is the same as base or a sub-domain of it.
// Used to refuse auto-patching headscale for TLDs that fall under MagicDNS's
// authoritative scope.
func tldIsUnder(tld, base string) bool {
	return tld == base || strings.HasSuffix(tld, "."+base)
}

// composeInstallPlan turns detected environment + user flags into the
// concrete InstallOptions. Explicit flags always win over detected values.
//
// Workloads always resolve via split-DNS (pier-dnsmasq + headscale config
// patch) regardless of whether a headscale records adapter is available —
// the records adapter is reserved for the dashboard FQDN, configured
// later by `pier serve install` and not at infra install time.
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
		plan.TLD = infra.DefaultTLD
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
	// Persist the headscale config path so Uninstall can revert the
	// split-DNS patch we apply post-Install. Requires the chosen TLD
	// to live OUTSIDE base_domain (MagicDNS pre-empts split-DNS for
	// names under base_domain).
	if env.Headscale.Found && env.Headscale.ConfigPath != "" &&
		env.Headscale.BaseDomain != "" && !tldIsUnder(plan.TLD, env.Headscale.BaseDomain) {
		plan.HeadscaleConfigPath = env.Headscale.ConfigPath
		plan.HeadscaleContainer = env.Headscale.Container
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

			// Load cfg BEFORE infra.Uninstall — it RemoveAll's paths.Root
			// at the end, taking config.toml with it. We need the
			// HeadscaleConfigPath/Container fields to revert the
			// split-DNS patch after infra is torn down.
			var cfg *infra.Config
			if paths, perr := infra.DefaultPaths(); perr == nil {
				cfg, _ = infra.LoadConfig(paths) // tolerate missing
			}

			infraTouched, err := infra.Uninstall(out, manualDNS)
			if err != nil {
				return err
			}

			headscaleTouched := revertHeadscalePatch(out, cfg)
			if removeOrphanDashboardRecord(out, cfg) {
				headscaleTouched = true
			}

			skillRemoved := false
			if dir, err := skill.UserDir(); err == nil {
				if removed := removeSkillLinks(out, dir); removed {
					skillRemoved = true
				}
				if removed, err := skill.Uninstall(dir); err != nil {
					fmt.Fprintf(out, "! skill removal failed: %v\n", err)
				} else if removed {
					skillRemoved = true
					fmt.Fprintf(out, "✓ AI skill removed: %s\n", dir)
				}
			}
			// Hint when there was nothing left to undo. Helps the user
			// distinguish a successful no-op (re-running uninstall) from
			// a silently broken command. Suppressed when --purge has work
			// to do — that path prints its own ✓ removed binary line.
			if !infraTouched && !headscaleTouched && !skillRemoved && !purge {
				fmt.Fprintln(out, "Nothing to uninstall — pier is already clean.")
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

func removeSkillLinks(out io.Writer, canonical string) bool {
	targets, err := skill.DetectedLinkTargets()
	if err != nil {
		fmt.Fprintf(out, "! skill links: %v (skipped)\n", err)
		return false
	}
	removedAny := false
	for _, target := range targets {
		status, err := skill.LinkState(target.Dir, canonical)
		if err != nil {
			fmt.Fprintf(out, "! %s skill link check failed: %v\n", target.Agent, err)
			continue
		}
		if status != skill.LinkCurrent {
			continue
		}
		if err := os.Remove(target.Dir); err != nil {
			fmt.Fprintf(out, "! remove %s skill link: %v\n", target.Agent, err)
			continue
		}
		removedAny = true
		fmt.Fprintf(out, "✓ %s skill link removed: %s\n", target.Agent, target.Dir)
	}
	return removedAny
}

// revertHeadscalePatch reverts the split-DNS rule pier added at install
// time and restarts the headscale container so peers re-sync. No-op
// when cfg is nil or HeadscaleConfigPath is unset (for example, a
// pre-existing install before this field was persisted). Best-effort:
// individual failures print a warning and a manual recovery hint
// rather than aborting the rest of uninstall.
//
// Returns true when at least the unpatch step actually changed the
// yaml, so the caller can suppress the "nothing to uninstall" hint
// when there was real work done.
func revertHeadscalePatch(out io.Writer, cfg *infra.Config) bool {
	if cfg == nil || cfg.HeadscaleConfigPath == "" || cfg.TLD == "" {
		return false
	}
	ip := cfg.EffectiveAnswerIP()
	changed, err := headscale.Unpatch(cfg.HeadscaleConfigPath, cfg.TLD, ip)
	if err != nil {
		fmt.Fprintf(out, "! headscale unpatch failed: %v\n", err)
		fmt.Fprintf(out, "  revert manually from %s.bak then `docker restart %s`\n",
			cfg.HeadscaleConfigPath, cfg.HeadscaleContainer)
		return false
	}
	if !changed {
		// Already clean (manual edit, or split-DNS Patch never applied).
		// Suppress noise — this is a no-op uninstall step.
		return false
	}
	fmt.Fprintf(out, "✓ reverted split-DNS patch in %s (.bak preserved)\n", cfg.HeadscaleConfigPath)
	if cfg.HeadscaleContainer != "" {
		if err := headscale.Reload(cfg.HeadscaleContainer); err != nil {
			fmt.Fprintf(out, "! headscale restart failed (%v) — restart manually: docker restart %s\n",
				err, cfg.HeadscaleContainer)
		} else {
			fmt.Fprintln(out, "✓ headscale restarted (DNS reload)")
		}
	}
	return true
}

// removeOrphanDashboardRecord cleans up the dashboard A record pier
// serve registers in headscale's extra_records when DashboardFQDN is
// configured under base_domain. pier serve normally removes its own
// record on graceful shutdown; this is the safety net for the cases
// where the daemon crashed, was killed without cleanup, or simply
// wasn't running at uninstall time. No-op when DashboardFQDN is empty
// (default pier.<TLD> never had a record) or when the records adapter
// path isn't configured.
//
// Returns true when something actually got removed so the caller can
// suppress the "nothing to uninstall" hint.
func removeOrphanDashboardRecord(out io.Writer, cfg *infra.Config) bool {
	if cfg == nil || cfg.DashboardFQDN == "" || cfg.HeadscaleRecordsPath == "" {
		return false
	}
	removed, err := headscale.Remove(cfg.HeadscaleRecordsPath, cfg.DashboardFQDN)
	if err != nil {
		fmt.Fprintf(out, "! headscale dashboard record cleanup %s: %v\n", cfg.DashboardFQDN, err)
		return false
	}
	if !removed {
		return false
	}
	fmt.Fprintf(out, "✓ removed orphan dashboard record %s from %s\n",
		cfg.DashboardFQDN, cfg.HeadscaleRecordsPath)
	return true
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
