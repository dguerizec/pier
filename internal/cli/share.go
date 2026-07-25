package cli

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/share"
	"github.com/dguerizec/pier/internal/state"
)

func newShareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Selectively share Pier hosts on a LAN",
	}
	cmd.AddCommand(
		newShareAddCmd(),
		newShareRemoveCmd(),
		newShareListCmd(),
		newShareHostsCmd(),
		newShareURLCmd(),
	)
	return cmd
}

type shareAddOpts struct {
	persist       bool
	interfaceName string
	bindIP        string
}

func newShareAddCmd() *cobra.Command {
	var opts shareAddOpts
	cmd := &cobra.Command{
		Use:   "add [PIER_HOST...]",
		Short: "Share selected hosts from the current worktree on a LAN address",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShareAdd(cmd, args, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.persist, "persist", false, "restore these shares when the LAN gateway or machine restarts")
	cmd.Flags().StringVar(&opts.interfaceName, "interface", "", "LAN interface to bind (interactive when omitted)")
	cmd.Flags().StringVar(&opts.bindIP, "bind-ip", "", "exact assigned LAN IPv4 address to bind")
	return cmd
}

func runShareAdd(cmd *cobra.Command, selectors []string, opts shareAddOpts) error {
	d, err := resolveDaily(cmd, "")
	if err != nil {
		return err
	}
	defer d.State.Close()

	if _, err := d.State.Get(d.Ctx.Project, d.Ctx.Slug); errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("workload %s is not running (run `pier up` first)", d.Ctx.Slug)
	} else if err != nil {
		return err
	}

	candidates := share.Candidates(d.Ctx)
	selected, err := resolveShareCandidates(cmd, candidates, selectors, d.Config.TLD)
	if err != nil {
		return err
	}
	address, err := resolveShareAddress(cmd, opts.interfaceName, opts.bindIP)
	if err != nil {
		return err
	}
	if err := validateShareAddress(d.Config, address); err != nil {
		return err
	}

	manager := share.NewManager(d.Paths.Root, d.Config.EffectiveTraefikNetwork())
	added, err := manager.Add(selected, address, opts.persist)
	if err != nil {
		return err
	}
	persistentCount := 0
	for _, record := range added {
		if record.Persistent {
			persistentCount++
		}
	}
	scope := "session"
	if persistentCount == len(added) {
		scope = "persistent"
	} else if persistentCount > 0 {
		scope = "mixed"
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "✓ sharing %d host(s) on %s:80 (%s)\n\n", len(added), address.IP, scope)
	fmt.Fprintln(out, "Add to the client’s /etc/hosts:")
	fmt.Fprintln(out)
	for _, record := range added {
		fmt.Fprintf(out, "%s %s\n", record.IP, record.Host)
	}
	return nil
}

func validateShareAddress(cfg *infra.Config, address share.Address) error {
	switch cfg.BindIP {
	case infra.DefaultServerBind:
		return fmt.Errorf("Pier's main proxy already binds 0.0.0.0:80, so every LAN interface exposes all Pier hosts; reconfigure it on a specific tailnet address before using selective sharing")
	case address.IP:
		return fmt.Errorf("Pier's main proxy already binds %s:80; choose a different LAN address for selective sharing", address.IP)
	default:
		return nil
	}
}

func resolveShareCandidates(cmd *cobra.Command, candidates []share.Candidate, selectors []string, tld string) ([]share.Candidate, error) {
	if len(selectors) > 0 {
		return share.SelectCandidates(candidates, selectors, tld)
	}
	if !commandIsInteractive(cmd) {
		return nil, errors.New("choose at least one PIER_HOST when stdin is not interactive")
	}
	var selected []string
	options := make([]huh.Option[string], 0, len(candidates))
	for _, candidate := range candidates {
		option := huh.NewOption(candidate.Host, candidate.Host)
		if candidate.Default {
			option = option.Selected(true)
			selected = append(selected, candidate.Host)
		}
		options = append(options, option)
	}
	field := huh.NewMultiSelect[string]().
		Title("Hosts to share").
		Description("Only selected exact hosts will be reachable from the LAN.").
		Options(options...).
		Value(&selected).
		Validate(func(values []string) error {
			if len(values) == 0 {
				return errors.New("select at least one host")
			}
			return nil
		})
	if err := huh.NewForm(huh.NewGroup(field)).Run(); err != nil {
		return nil, fmt.Errorf("host prompt: %w", err)
	}
	return share.SelectCandidates(candidates, selected, tld)
}

func resolveShareAddress(cmd *cobra.Command, interfaceName, bindIP string) (share.Address, error) {
	addresses, err := share.LANAddresses()
	if err != nil {
		return share.Address{}, err
	}
	if interfaceName != "" || bindIP != "" {
		return share.ResolveAddress(addresses, interfaceName, bindIP)
	}
	if !commandIsInteractive(cmd) {
		return share.Address{}, errors.New("choose a LAN address with --interface or --bind-ip when stdin is not interactive")
	}
	if len(addresses) == 0 {
		return share.Address{}, errors.New("no active LAN IPv4 address found")
	}
	selected := addresses[0]
	options := make([]huh.Option[share.Address], 0, len(addresses))
	for _, address := range addresses {
		options = append(options, huh.NewOption(
			fmt.Sprintf("%s — %s", address.Interface, address.IP),
			address,
		))
	}
	field := huh.NewSelect[share.Address]().
		Title("LAN address").
		Description("Pier binds this exact address and will not follow the interface to another network.").
		Options(options...).
		Value(&selected)
	if err := huh.NewForm(huh.NewGroup(field)).Run(); err != nil {
		return share.Address{}, fmt.Errorf("LAN interface prompt: %w", err)
	}
	return selected, nil
}

func newShareRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove [PIER_HOST...]",
		Short: "Stop sharing selected hosts from the current worktree",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShareRemove(cmd, args)
		},
	}
	return cmd
}

func runShareRemove(cmd *cobra.Command, selectors []string) error {
	d, err := resolveDaily(cmd, "")
	if err != nil {
		return err
	}
	defer d.State.Close()
	manager := share.NewManager(d.Paths.Root, d.Config.EffectiveTraefikNetwork())
	records, err := manager.Stored()
	if err != nil {
		return err
	}
	records = scopeSharedRecords(records, d.Ctx.Project, d.Ctx.Slug, false)
	if len(records) == 0 {
		return errors.New("no shared hosts for the current worktree")
	}
	selected, err := resolveSharedRecords(cmd, records, selectors, d.Config.TLD, "Hosts to stop sharing")
	if err != nil {
		return err
	}
	hosts := sharedHosts(selected)
	removed, err := manager.Remove(d.Ctx.Project, d.Ctx.Slug, hosts)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ stopped sharing %d host(s)\n", removed)
	return nil
}

func resolveSharedRecords(cmd *cobra.Command, records []share.SharedRecord, selectors []string, tld, title string) ([]share.SharedRecord, error) {
	if len(selectors) > 0 {
		return share.SelectRecords(records, selectors, tld)
	}
	if !commandIsInteractive(cmd) {
		return nil, errors.New("choose at least one PIER_HOST when stdin is not interactive")
	}
	var selected []string
	options := make([]huh.Option[string], 0, len(records))
	for _, record := range records {
		options = append(options, huh.NewOption(record.Host, record.Host))
	}
	field := huh.NewMultiSelect[string]().
		Title(title).
		Options(options...).
		Value(&selected).
		Validate(func(values []string) error {
			if len(values) == 0 {
				return errors.New("select at least one host")
			}
			return nil
		})
	if err := huh.NewForm(huh.NewGroup(field)).Run(); err != nil {
		return nil, err
	}
	return share.SelectRecords(records, selected, tld)
}

func newShareListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List shared hosts and their live status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShareList(cmd, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list shares from every Pier worktree")
	return cmd
}

func runShareList(cmd *cobra.Command, all bool) error {
	d, records, err := loadSharedRecords(cmd, all)
	if err != nil {
		return err
	}
	defer d.State.Close()
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "HOST\tBIND\tSCOPE\tSTATUS")
	for _, record := range records {
		status := "reachable"
		switch {
		case !record.AddressUp:
			status = "address unavailable"
		case !record.GatewayUp:
			status = "gateway down"
		case !record.WorkloadUp:
			status = "workload down"
		}
		scope := "session"
		if record.Persistent {
			scope = "persistent"
		}
		fmt.Fprintf(writer, "%s\t%s/%s\t%s\t%s\n", record.Host, record.Interface, record.IP, scope, status)
	}
	return writer.Flush()
}

func newShareHostsCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "hosts [PIER_HOST...]",
		Short: "Print active shares as paste-ready /etc/hosts lines",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShareHosts(cmd, args, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include shares from every Pier worktree")
	return cmd
}

func runShareHosts(cmd *cobra.Command, selectors []string, all bool) error {
	d, records, err := loadSharedRecords(cmd, all)
	if err != nil {
		return err
	}
	defer d.State.Close()
	if len(selectors) > 0 {
		records, err = share.SelectRecords(records, selectors, d.Config.TLD)
		if err != nil {
			return err
		}
	}
	active, omitted := activeSharedRecords(records)
	for _, record := range active {
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", record.IP, record.Host)
	}
	if omitted > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %d share(s) omitted because their LAN address or gateway is unavailable\n", omitted)
	}
	return nil
}

func newShareURLCmd() *cobra.Command {
	var (
		explicitDefault bool
		all             bool
	)
	cmd := &cobra.Command{
		Use:   "url [PIER_HOST...]",
		Short: "Print shared URL(s) for the current worktree",
		Args:  cobra.ArbitraryArgs,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if explicitDefault && all {
				return errors.New("--default and --all are mutually exclusive")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShareURL(cmd, args, explicitDefault, all)
		},
	}
	cmd.Flags().BoolVar(&explicitDefault, "default", false, "print exactly the shared entry-point URL")
	cmd.Flags().BoolVar(&all, "all", false, "print every shared URL")
	return cmd
}

func runShareURL(cmd *cobra.Command, selectors []string, explicitDefault, all bool) error {
	d, records, err := loadSharedRecords(cmd, false)
	if err != nil {
		return err
	}
	defer d.State.Close()
	if len(selectors) > 0 {
		records, err = share.SelectRecords(records, selectors, d.Config.TLD)
		if err != nil {
			return err
		}
	}
	records, omitted := activeSharedRecords(records)
	if omitted > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %d share(s) omitted because their LAN address or gateway is unavailable\n", omitted)
	}

	if !all && len(selectors) == 0 {
		for _, record := range records {
			if record.Default {
				fmt.Fprintln(cmd.OutOrStdout(), sharedURL(record.Host))
				return nil
			}
		}
		defaultHost := strings.TrimPrefix(adapter.DefaultURL(d.Ctx), "http://")
		return fmt.Errorf("default URL %s is not shared\nhint: pier share add %s", defaultHost, defaultHost)
	}
	if explicitDefault {
		for _, record := range records {
			if record.Default {
				fmt.Fprintln(cmd.OutOrStdout(), sharedURL(record.Host))
				return nil
			}
		}
		return errors.New("the selected shared hosts do not include the default URL")
	}
	for _, record := range records {
		fmt.Fprintln(cmd.OutOrStdout(), sharedURL(record.Host))
	}
	return nil
}

func loadSharedRecords(cmd *cobra.Command, all bool) (*daily, []share.SharedRecord, error) {
	d, err := resolveDaily(cmd, "")
	if err != nil {
		return nil, nil, err
	}
	manager := share.NewManager(d.Paths.Root, d.Config.EffectiveTraefikNetwork())
	records, err := manager.List()
	if err != nil {
		d.State.Close()
		return nil, nil, err
	}
	addresses, err := share.LANAddresses()
	if err != nil {
		d.State.Close()
		return nil, nil, err
	}
	available := make(map[share.Address]bool, len(addresses))
	for _, address := range addresses {
		available[address] = true
	}
	for i := range records {
		records[i].AddressUp = available[records[i].Address]
	}
	return d, scopeSharedRecords(records, d.Ctx.Project, d.Ctx.Slug, all), nil
}

func scopeSharedRecords(records []share.SharedRecord, project, slug string, all bool) []share.SharedRecord {
	if all {
		return records
	}
	out := records[:0]
	for _, record := range records {
		if record.Project == project && record.Slug == slug {
			out = append(out, record)
		}
	}
	return out
}

func activeSharedRecords(records []share.SharedRecord) ([]share.SharedRecord, int) {
	out := records[:0]
	omitted := 0
	for _, record := range records {
		if !record.AddressUp || !record.GatewayUp {
			omitted++
			continue
		}
		out = append(out, record)
	}
	return out, omitted
}

func sharedHosts(records []share.SharedRecord) []string {
	hosts := make([]string, 0, len(records))
	for _, record := range records {
		hosts = append(hosts, record.Host)
	}
	sort.Strings(hosts)
	return hosts
}

func sharedURL(host string) string {
	return (&url.URL{Scheme: "http", Host: host}).String()
}
