package cli

import (
	"errors"

	"github.com/spf13/cobra"
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
			return errors.New("install: not implemented yet")
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.mode, "mode", "", "local | server")
	f.StringVar(&opts.tld, "tld", "test", "base TLD (RFC2606 reserved recommended)")
	f.BoolVar(&opts.manualDNS, "manual-dns", false, "skip system DNS modification, print instructions instead")
	f.BoolVar(&opts.noSudo, "no-sudo", false, "alias of --manual-dns")
	f.StringVar(&opts.bindIP, "bind-ip", "", "traefik/dnsmasq bind IP (server mode, default 0.0.0.0)")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop infra containers, remove resolver files, clear config dir",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("uninstall: not implemented yet")
		},
	}
}
