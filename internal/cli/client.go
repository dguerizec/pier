package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Configure this machine to reach a remote server-mode pier",
	}
	cmd.AddCommand(newClientAddCmd(), newClientTailscaleCmd())
	return cmd
}

type clientAddOpts struct {
	tld      string
	resolver string
}

func newClientAddCmd() *cobra.Command {
	var opts clientAddOpts
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a per-domain resolver pointing at a remote pier server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("client add: not implemented yet")
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.tld, "tld", "test", "TLD to route to the remote resolver")
	f.StringVar(&opts.resolver, "resolver", "", "remote resolver IP (required)")
	_ = cmd.MarkFlagRequired("resolver")
	return cmd
}

func newClientTailscaleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tailscale",
		Short: "One-shot tailscale split-DNS setup for a remote pier server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("client tailscale: not implemented yet")
		},
	}
}
