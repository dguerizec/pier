package cli

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/detect"
	"github.com/LeoPartt/pier/internal/infra"
)

type installOpts struct {
	mode            string
	tld             string
	manualDNS       bool
	noSudo          bool
	bindIP          string
	answerIP        string
	externalTraefik string
	traefikNetwork  string
	yes             bool
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
			return infra.Install(infra.InstallOptions{
				Mode:            mode,
				TLD:             opts.tld,
				BindIP:          opts.bindIP,
				AnswerIP:        opts.answerIP,
				ManualDNS:       opts.manualDNS,
				Out:             cmd.OutOrStdout(),
				ExternalTraefik: opts.externalTraefik,
				TraefikNetwork:  opts.traefikNetwork,
			})
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
	f.BoolVarP(&opts.yes, "yes", "y", false, "accept the detected wizard plan without prompting")
	return cmd
}

// shouldRunWizard reports whether to enter the auto-detect wizard. We do so
// when none of the install-shape flags are present — implying the user
// expects pier to figure things out — and explicit configuration takes
// precedence whenever any of those flags is set.
func shouldRunWizard(cmd *cobra.Command) bool {
	for _, name := range []string{"mode", "bind-ip", "answer-ip", "use-existing-traefik", "traefik-network"} {
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

	plan := composeInstallPlan(env, base)
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

	if env.Headscale.Found && env.Tailscale.Active && env.Headscale.ConfigPath != "" {
		fmt.Fprintln(out)
		if base.yes || confirm(cmd.InOrStdin(), out, fmt.Sprintf("Patch %s with .%s split-DNS?", env.Headscale.ConfigPath, plan.TLD), true) {
			fmt.Fprintf(out, "(headscale auto-patch lands in the next commit; for now run `pier client tailscale` and copy the snippet manually)\n")
		}
	}
	return nil
}

// composeInstallPlan turns detected environment + user flags into the
// concrete InstallOptions. Explicit flags always win over detected values.
func composeInstallPlan(env detect.Environment, base installOpts) infra.InstallOptions {
	plan := infra.InstallOptions{
		Mode:            base.mode,
		TLD:             base.tld,
		BindIP:          base.bindIP,
		AnswerIP:        base.answerIP,
		ManualDNS:       base.manualDNS,
		ExternalTraefik: base.externalTraefik,
		TraefikNetwork:  base.traefikNetwork,
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
	if env.Traefik.Found {
		if plan.ExternalTraefik == "" {
			plan.ExternalTraefik = env.Traefik.Container
		}
		if plan.TraefikNetwork == "" {
			plan.TraefikNetwork = env.Traefik.Network
		}
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
	return strings.Join(parts, " ")
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
	var manualDNS bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop infra containers, remove resolver files, clear config dir",
		RunE: func(cmd *cobra.Command, args []string) error {
			return infra.Uninstall(cmd.OutOrStdout(), manualDNS)
		},
	}
	cmd.Flags().BoolVar(&manualDNS, "manual-dns", false, "do not touch host DNS, print revert instructions instead")
	return cmd
}
