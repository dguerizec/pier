package cli

import (
	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/infra"
)

type installOpts struct {
	mode      string
	tld       string
	manualDNS bool
	noSudo    bool
	bindIP    string
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
			mode := opts.mode
			if mode == "" {
				mode = infra.ModeLocal
			}
			return infra.Install(infra.InstallOptions{
				Mode:      mode,
				TLD:       opts.tld,
				BindIP:    opts.bindIP,
				ManualDNS: opts.manualDNS,
				Out:       cmd.OutOrStdout(),
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.mode, "mode", "", "local | server (only local supported in MVP)")
	f.StringVar(&opts.tld, "tld", infra.DefaultTLD, "base TLD (RFC2606 reserved recommended)")
	f.BoolVar(&opts.manualDNS, "manual-dns", false, "skip system DNS modification, print instructions instead")
	f.BoolVar(&opts.noSudo, "no-sudo", false, "alias of --manual-dns")
	f.StringVar(&opts.bindIP, "bind-ip", "", "traefik/dnsmasq bind IP (server mode, default 0.0.0.0)")
	return cmd
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
