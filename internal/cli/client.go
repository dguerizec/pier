package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/infra"
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
	apply    bool
}

func newClientAddCmd() *cobra.Command {
	var opts clientAddOpts
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Route .<tld> queries on this machine to <resolver>",
		Long: `Adds a per-domain DNS rule so this machine resolves .<tld> via <resolver>
(typically the LAN/Tailscale IP of a server-mode pier host). Without --apply
the command prints the platform-specific instructions instead of mutating
the system; pass --apply to install the drop-in via sudo.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClientAdd(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.tld, "tld", infra.DefaultTLD, "TLD to route")
	f.StringVar(&opts.resolver, "resolver", "", "resolver IP (required)")
	f.BoolVar(&opts.apply, "apply", false, "actually mutate the system (sudo prompt) instead of printing instructions")
	_ = cmd.MarkFlagRequired("resolver")
	return cmd
}

func runClientAdd(cmd *cobra.Command, opts clientAddOpts) error {
	out := cmd.OutOrStdout()

	switch runtime.GOOS {
	case "linux":
		return clientAddLinux(out, opts)
	case "darwin":
		return clientAddMacOS(out, opts)
	default:
		return fmt.Errorf("client add: %s is not supported yet", runtime.GOOS)
	}
}

func clientAddLinux(out io.Writer, opts clientAddOpts) error {
	if !systemdResolvedActive() {
		fmt.Fprintf(out, "systemd-resolved is not active on this host. ")
		if tailscaleActive() {
			fmt.Fprintln(out, "Tailscale appears to manage /etc/resolv.conf — try `pier client tailscale` for split-DNS guidance.")
		} else {
			fmt.Fprintln(out, "Either enable systemd-resolved or edit /etc/resolv.conf manually with `nameserver "+opts.resolver+"` and `search "+opts.tld+"`.")
		}
		return nil
	}

	dropin := fmt.Sprintf("# managed by pier client\n[Resolve]\nDNS=%s\nDomains=~%s\n", opts.resolver, opts.tld)
	dropinPath := "/etc/systemd/resolved.conf.d/pier-client.conf"

	if !opts.apply {
		fmt.Fprintf(out, `Run as root to route .%s queries to %s:

  sudo tee %s >/dev/null <<'EOF'
%s
EOF
  sudo systemctl reload-or-restart systemd-resolved

Then verify:  dig +short %s.%s
`, opts.tld, opts.resolver, dropinPath, dropin, "anything", opts.tld)
		return nil
	}
	fmt.Fprintln(out, "(--apply not yet implemented for client add on Linux; print-only for now)")
	return nil
}

func clientAddMacOS(out io.Writer, opts clientAddOpts) error {
	resolverPath := "/etc/resolver/" + opts.tld
	body := "nameserver " + opts.resolver + "\n"

	if !opts.apply {
		fmt.Fprintf(out, `Run as root to route .%s queries to %s on macOS:

  sudo mkdir -p /etc/resolver
  echo '%s' | sudo tee %s

Then verify:  scutil --dns | grep -A 2 %s
`, opts.tld, opts.resolver, strings.TrimSpace(body), resolverPath, opts.tld)
		return nil
	}
	fmt.Fprintln(out, "(--apply not yet implemented for client add on macOS; print-only for now)")
	return nil
}

func newClientTailscaleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tailscale",
		Short: "Diagnose tailscale + print exact split-DNS instructions for .<tld>",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClientTailscale(cmd)
		},
	}
}

func runClientTailscale(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	if !tailscaleActive() {
		return errors.New("tailscale not detected on this host")
	}

	status, err := tailscaleStatus()
	if err != nil {
		fmt.Fprintf(out, "warning: could not read tailscale status (%v) — continuing with generic guidance\n\n", err)
	}

	paths, err := infra.DefaultPaths()
	if err != nil {
		return err
	}
	cfg, err := infra.LoadConfig(paths)
	if err != nil {
		fmt.Fprintln(out, "pier is not installed on this host yet — `pier install --mode server` is the usual prerequisite for `client tailscale`.")
		return nil
	}

	selfIP := ""
	tailnetName := ""
	if status != nil {
		selfIP = status.SelfIP()
		tailnetName = status.tailnetName()
	}

	fmt.Fprintf(out, "Detected: tailscale (this host: %s, tailnet: %s)\n", emptyAs(selfIP, "?"), emptyAs(tailnetName, "?"))
	fmt.Fprintf(out, "Pier on this host: mode=%s, tld=.%s, bind=%s\n\n", cfg.Mode, cfg.TLD, cfg.BindIP)

	resolverIP := selfIP
	if cfg.Mode == infra.ModeLocal {
		fmt.Fprintf(out, "Pier is in --mode local on this host (binding 127.0.0.1). For tailnet-wide split-DNS, pier should be installed in --mode server so peers can reach the resolver. Re-run:\n\n  pier uninstall && pier install --mode server\n\n")
		resolverIP = "<server-ip>"
	}

	fmt.Fprintf(out, `To route .%s queries from every tailnet peer to this host's pier-dnsmasq:

Tailscale (SaaS) — admin panel:
  https://login.tailscale.com/admin/dns
  Add a custom resolver:
    domain     = %s
    nameserver = %s

Headscale 0.26+ (self-hosted) — config.yaml:
  dns:
    nameservers:
      split:
        %s:
          - %s
    search_domains:
      - %s

Headscale ≤ 0.25 (legacy schema):
  dns_config:
    restricted_nameservers:
      %s:
        - %s
    domains:
      - %s

Per-machine override (if you control resolv.conf yourself):
  pier client add --tld %s --resolver %s --apply

Or just rerun "pier install" — the wizard will offer to auto-patch headscale config.yaml on hosts where headscale runs locally.
`, cfg.TLD, cfg.TLD, resolverIP, cfg.TLD, resolverIP, cfg.TLD, cfg.TLD, resolverIP, cfg.TLD, cfg.TLD, resolverIP)
	return nil
}

// tailscaleActive reports whether the tailscale CLI exists and the daemon
// answers `status`.
func tailscaleActive() bool {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return false
	}
	return exec.Command("tailscale", "status").Run() == nil
}

// tsStatus is a partial mirror of `tailscale status --json` (only the
// fields we use). Tailscale guarantees these keys across versions.
type tsStatus struct {
	Self struct {
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
	MagicDNSSuffix string `json:"MagicDNSSuffix"`
	CurrentTailnet struct {
		Name string `json:"Name"`
	} `json:"CurrentTailnet"`
}

func tailscaleStatus() (*tsStatus, error) {
	cmd := exec.Command("tailscale", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var s tsStatus
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *tsStatus) SelfIP() string {
	for _, ip := range s.Self.TailscaleIPs {
		if !strings.Contains(ip, ":") { // skip IPv6
			return ip
		}
	}
	return ""
}

func (s *tsStatus) tailnetName() string {
	if s.CurrentTailnet.Name != "" {
		return s.CurrentTailnet.Name
	}
	return strings.TrimSuffix(s.MagicDNSSuffix, ".")
}

func systemdResolvedActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", "systemd-resolved").Run() == nil
}

func emptyAs(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
