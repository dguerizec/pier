package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/detect"
	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/systemd"
)

func newServeInstallCmd() *cobra.Command {
	var (
		printOnly     bool
		dashboardFQDN string
		yes           bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install pier serve as a systemd --user unit",
		Long: `Writes a pier.service unit under ~/.config/systemd/user/ and runs
'systemctl --user daemon-reload && enable --now'.

Before installing the unit, prompts (interactively) for a dashboard
FQDN: the default 'pier.<TLD>' is covered by the split-DNS wildcard
and needs nothing extra; opting in to a hostname under your headscale
base_domain (e.g. 'pier.<base_domain>') routes the dashboard through
headscale's extra_records adapter so the URL lives next to your
prod hostnames.

Pass --print-only to skip exec and print the systemctl commands
instead — useful in CI, scripted bootstraps, or when you route
privilege escalation through your own tooling. Pass --yes (or pipe
stdin) to accept the 'pier.<TLD>' default non-interactively. Pass
--dashboard-fqdn to set the FQDN explicitly without a prompt.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServeInstall(cmd, dashboardFQDN, yes, printOnly)
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print-only", false, "print the systemctl commands instead of running them")
	cmd.Flags().StringVar(&dashboardFQDN, "dashboard-fqdn", "", "dashboard hostname (default: pier.<TLD>; under base_domain to use headscale extra_records)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept defaults non-interactively (skip dashboard FQDN prompt)")
	return cmd
}

func runServeInstall(cmd *cobra.Command, dashboardFQDN string, yes bool, printOnly bool) error {
	out := cmd.OutOrStdout()

	paths, err := infra.DefaultPaths()
	if err != nil {
		return err
	}
	cfg, err := infra.LoadConfig(paths)
	if err != nil {
		return err
	}

	env := detect.Run()
	fqdn, hsContainer, hsRecordsPath, err := resolveDashboardFQDN(cmd, cfg, env, dashboardFQDN, yes)
	if err != nil {
		return err
	}

	// Persist whatever the resolution returned. fqdn is always non-empty
	// (default fallback is pier.<TLD>); hs* are populated only when the
	// FQDN lives under base_domain and the records adapter is needed.
	dirty := false
	if fqdn != cfg.DashboardFQDN {
		cfg.DashboardFQDN = fqdn
		dirty = true
	}
	if hsContainer != "" && hsContainer != cfg.HeadscaleContainer {
		cfg.HeadscaleContainer = hsContainer
		dirty = true
	}
	if hsRecordsPath != cfg.HeadscaleRecordsPath {
		// Allow dropping the field too: an FQDN that no longer needs
		// the records adapter (default pier.<TLD>) clears it so the
		// next pier serve doesn't try to publish a record.
		cfg.HeadscaleRecordsPath = hsRecordsPath
		dirty = true
	}
	if dirty {
		if err := cfg.Save(paths); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		fmt.Fprintf(out, "✓ dashboard FQDN: %s\n", fqdn)
		if hsRecordsPath != "" {
			fmt.Fprintf(out, "  records adapter: %s (container: %s)\n", hsRecordsPath, hsContainer)
		}
	}

	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	_, err = systemd.Install(bin, printOnly, out)
	return err
}

// resolveDashboardFQDN picks the dashboard FQDN from the (flag, --yes,
// TTY interactive) decision tree and validates the result.
//
// Returns:
//   - fqdn: always non-empty (defaults to pier.<TLD> when nothing else set)
//   - hsContainer / hsRecordsPath: set only when the chosen FQDN needs
//     headscale's extra_records adapter (i.e. lives under base_domain).
//     Empty when the FQDN is under cfg.TLD (split-DNS wildcard covers).
func resolveDashboardFQDN(cmd *cobra.Command, cfg *infra.Config, env detect.Environment, flag string, yes bool) (string, string, string, error) {
	if cfg.TLD == "" {
		return "", "", "", errors.New("config has empty TLD; re-run pier install first")
	}
	defaultFQDN := "pier." + cfg.TLD

	// 1) Explicit flag wins. Validate strictly — bad input is better
	//    surfaced as a startup error than a silent no-op at serve time.
	if flag != "" {
		return validateDashboardFQDN(flag, cfg.TLD, env)
	}

	// 2) Non-interactive: --yes flag or piped stdin → take default.
	if yes || !serveInstallIsInteractive(cmd) {
		return defaultFQDN, "", "", nil
	}

	// 3) Interactive: only offer the choice when records adapter
	//    is actually available. Otherwise default silently.
	if env.Headscale.Found && env.Headscale.RecordsPath != "" && env.Headscale.BaseDomain != "" &&
		!strings.HasSuffix(env.Headscale.BaseDomain, "."+cfg.TLD) && cfg.TLD != env.Headscale.BaseDomain {
		baseDomainFQDN := "pier." + env.Headscale.BaseDomain
		choice := defaultFQDN
		sel := huh.NewSelect[string]().
			Title("Dashboard FQDN").
			Description("Where pier serve publishes the admin dashboard.").
			Options(
				huh.NewOption(fmt.Sprintf("%s  (split-DNS wildcard, no records adapter)", defaultFQDN), defaultFQDN),
				huh.NewOption(fmt.Sprintf("%s  (under your tailnet zone via headscale extra_records)", baseDomainFQDN), baseDomainFQDN),
			).
			Value(&choice)
		if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
			return "", "", "", fmt.Errorf("dashboard prompt: %w", err)
		}
		if choice == baseDomainFQDN {
			return baseDomainFQDN, env.Headscale.Container, env.Headscale.RecordsPath, nil
		}
	}
	return defaultFQDN, "", "", nil
}

// validateDashboardFQDN ensures the user-supplied FQDN can actually be
// served. Two valid shapes:
//   - under pier's TLD → covered by the split-DNS wildcard, no
//     records adapter needed
//   - under headscale's base_domain → needs the records adapter; we
//     return the matching container + path
//
// Anything else (bare hostname, public domain, sibling TLD) is
// rejected with a hint so the user can pick a valid placement.
func validateDashboardFQDN(fqdn, tld string, env detect.Environment) (string, string, string, error) {
	fqdn = strings.TrimSpace(fqdn)
	if fqdn == "" {
		return "", "", "", errors.New("--dashboard-fqdn is empty")
	}
	if strings.HasSuffix(fqdn, "."+tld) {
		// Under pier TLD: wildcard covers, no records adapter needed.
		return fqdn, "", "", nil
	}
	if env.Headscale.Found && env.Headscale.BaseDomain != "" &&
		strings.HasSuffix(fqdn, "."+env.Headscale.BaseDomain) {
		if env.Headscale.RecordsPath == "" {
			return "", "", "", fmt.Errorf("FQDN %q is under headscale base_domain %q but extra_records_path is not configured — set dns.extra_records_path in headscale.yaml so pier can publish the record", fqdn, env.Headscale.BaseDomain)
		}
		if env.Headscale.Container == "" {
			return "", "", "", fmt.Errorf("FQDN %q is under headscale base_domain but the headscale container could not be detected", fqdn)
		}
		return fqdn, env.Headscale.Container, env.Headscale.RecordsPath, nil
	}
	hint := "."+tld
	if env.Headscale.Found && env.Headscale.BaseDomain != "" {
		hint = "."+tld+" or ."+env.Headscale.BaseDomain
	}
	return "", "", "", fmt.Errorf("FQDN %q must live under %s", fqdn, hint)
}

// serveInstallIsInteractive treats the call as interactive when stdin
// AND stdout are both terminals. Mirrors initwizard.IsInteractive
// semantics; kept package-local to avoid the cli → initwizard import.
func serveInstallIsInteractive(cmd *cobra.Command) bool {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return false
	}
	out, ok := cmd.OutOrStdout().(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(in.Fd()) && isatty.IsTerminal(out.Fd())
}

func newServeUninstallCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the pier serve systemd --user unit",
		RunE: func(cmd *cobra.Command, args []string) error {
			return systemd.Uninstall(printOnly, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print-only", false, "print the systemctl commands instead of running them")
	return cmd
}
