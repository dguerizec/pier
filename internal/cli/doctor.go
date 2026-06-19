package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/manifest"
	"github.com/dguerizec/pier/internal/state"
	"github.com/dguerizec/pier/internal/systemd"
)

func newDoctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose pier infra and active workloads",
		Long: `Walks the install + state and reports anything broken.
Use --fix to attempt automatic recovery (restart containers, reload host DNS,
drop dead workload rows).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			report := infra.Diagnose()
			if !fix {
				appendServeUnitChecks(&report)
				appendLegacySystemUnitCheck(&report)
				appendStateChecks(&report)
				report.Print(out)
				if report.HasFailures() {
					return fmt.Errorf("doctor: some checks failed")
				}
				return nil
			}

			// --fix path: run infra fix first, then state pruning, then
			// re-diagnose for a clean final report.
			fixed := infra.Fix()
			pruned := pruneDeadWorkloads()
			fixed.Actions = append(fixed.Actions, pruned...)
			rea := reattachLeakyAliases()
			fixed.Actions = append(fixed.Actions, rea...)
			refreshed := refreshDeadRoutes()
			fixed.Actions = append(fixed.Actions, refreshed...)
			appendServeUnitChecks(&fixed)
			appendLegacySystemUnitCheck(&fixed)
			appendStateChecks(&fixed)
			fixed.Print(out)
			if fixed.HasFailures() {
				return fmt.Errorf("doctor: failures remain after --fix")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "attempt to recover from failures (restart containers, reload DNS, drop dead state rows)")
	return cmd
}

// appendServeUnitChecks reports the pier.service --user unit's state
// when one is installed. Skipped silently otherwise — `pier serve`
// runs fine without one.
func appendServeUnitChecks(r *infra.Report) {
	st := systemd.Query()
	if !st.Loaded {
		return
	}
	name := "systemd unit pier.service (--user)"
	switch {
	case st.Active && st.Enabled:
		r.Checks = append(r.Checks, infra.Check{Name: name, Status: infra.StatusPass, Detail: "active, enabled"})
	case st.Active && !st.Enabled:
		r.Checks = append(r.Checks, infra.Check{Name: name, Status: infra.StatusWarn, Detail: "active but not enabled — won't restart on boot"})
	case !st.Active:
		detail := st.Detail
		if detail == "" {
			detail = "inactive"
		}
		r.Checks = append(r.Checks, infra.Check{
			Name:    name,
			Status:  infra.StatusFail,
			Detail:  detail,
			FixHint: "systemctl --user start pier",
		})
	}
}

// appendLegacySystemUnitCheck reports the pre-user-unit system service. It is
// not auto-fixed because removing files under /etc/systemd requires sudo.
func appendLegacySystemUnitCheck(r *infra.Report) {
	st := systemd.QuerySystem()
	if !st.Loaded {
		return
	}
	status := infra.StatusWarn
	detail := "legacy system unit found at " + st.Path + "; pier now uses `systemctl --user`"
	if st.Active {
		status = infra.StatusFail
		if st.Detail != "" {
			detail += " and the system unit is " + st.Detail
		}
	} else if st.Enabled {
		detail += " and is enabled"
	}
	r.Checks = append(r.Checks, infra.Check{
		Name:    "legacy systemd unit pier.service (system)",
		Status:  status,
		Detail:  detail,
		FixHint: "sudo systemctl disable --now pier.service && sudo rm -f /etc/systemd/system/pier.service && sudo systemctl daemon-reload",
	})
}

// appendStateChecks adds one check per workload row, marking rows whose
// container is gone as failures, or whose pier-network aliases leak the
// compose service short name as warnings (cross-project collision).
func appendStateChecks(r *infra.Report) {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return
	}
	cfg, err := infra.LoadConfig(paths)
	network := ""
	if err == nil {
		network = cfg.EffectiveTraefikNetwork()
	}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		r.Checks = append(r.Checks, infra.Check{
			Name:   "state DB",
			Status: infra.StatusFail,
			Detail: err.Error(),
		})
		return
	}
	defer store.Close()

	workloads, err := store.List()
	if err != nil {
		r.Checks = append(r.Checks, infra.Check{Name: "state DB list", Status: infra.StatusFail, Detail: err.Error()})
		return
	}
	r.Checks = append(r.Checks, infra.Check{Name: fmt.Sprintf("state DB (%d workload(s))", len(workloads)), Status: infra.StatusPass})

	for _, w := range workloads {
		name := fmt.Sprintf("workload %s/%s", w.Project, w.Slug)
		if w.ContainerID == "" {
			r.Checks = append(r.Checks, infra.Check{Name: name, Status: infra.StatusWarn, Detail: "no container_id recorded — state row may be stale"})
			continue
		}
		if !containerAlive(w.ContainerID) {
			r.Checks = append(r.Checks, infra.Check{
				Name:    name,
				Status:  infra.StatusFail,
				Detail:  "container " + short(w.ContainerID) + " not found",
				FixHint: "pier doctor --fix  (will drop the orphan row)",
			})
			continue
		}
		if leaks := leakyPierAliases(w.ContainerID, network); len(leaks) > 0 {
			r.Checks = append(r.Checks, infra.Check{
				Name:   name,
				Status: infra.StatusFail,
				Detail: fmt.Sprintf("short alias%s on %s network: %s — collides with sibling projects",
					pluralS(len(leaks)), network, strings.Join(leaks, ", ")),
				FixHint: "pier doctor --fix  (will reattach with FQDN-only aliases)",
			})
			continue
		}
		if detail := workloadRouteFailure(w, cfg.TLD); detail != "" {
			r.Checks = append(r.Checks, infra.Check{
				Name:    name,
				Status:  infra.StatusFail,
				Detail:  detail,
				FixHint: "pier doctor --fix  (will refresh traefik routes)",
			})
			continue
		}
		r.Checks = append(r.Checks, infra.Check{Name: name, Status: infra.StatusPass})
	}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

// leakyPierAliases returns the short (dot-less) aliases the container
// carries on the shared pier network. Any value here is a leak: docker
// auto-registers compose service names (`backend`, `frontend`, …) on
// every network the service joins, which collides across projects on
// the shared pier net. Pier attaches the shared network itself, with
// --alias <FQDN> only, to avoid this — but a `docker compose restart`
// outside pier (or a pre-fix workload) reintroduces the short alias.
//
// Returns nil when the container isn't on the pier network at all, or
// when its aliases are clean.
func leakyPierAliases(containerID, network string) []string {
	format := fmt.Sprintf(`{{json (index .NetworkSettings.Networks %q).Aliases}}`, network)
	out, err := exec.Command("docker", "inspect", "--format", format, containerID).Output()
	if err != nil {
		// Container isn't on the pier network — separate concern; we
		// don't surface it here because traefik would already show a
		// 502 and the user notices.
		return nil
	}
	var aliases []string
	if err := json.Unmarshal(out, &aliases); err != nil || len(aliases) == 0 {
		return nil
	}
	var bad []string
	for _, a := range aliases {
		if !strings.Contains(a, ".") {
			bad = append(bad, a)
		}
	}
	return bad
}

// reattachLeakyAliases re-runs pier's network attachment for every
// workload whose pier-network aliases include a short (collision-risk)
// name. It reads each workload's manifest to recover the exposed
// service list, then defers to adapter.AttachToTraefikNetwork.
func reattachLeakyAliases() []string {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return nil
	}
	cfg, err := infra.LoadConfig(paths)
	if err != nil {
		return nil
	}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		return nil
	}
	defer store.Close()

	workloads, err := store.List()
	if err != nil {
		return nil
	}
	network := cfg.EffectiveTraefikNetwork()
	var actions []string
	for _, w := range workloads {
		if w.ContainerID == "" || !containerAlive(w.ContainerID) {
			continue
		}
		if len(leakyPierAliases(w.ContainerID, network)) == 0 {
			continue
		}
		m, err := manifest.Load(w.WorktreePath)
		if err != nil {
			continue
		}
		baseDomain := m.Project.BaseDomain
		if baseDomain == "" {
			baseDomain = m.Project.Name + "." + cfg.TLD
		} else {
			expanded, err := adapter.ExpandPierTokens(baseDomain, cfg.TLD)
			if err != nil {
				continue
			}
			baseDomain = expanded
		}
		defaultService := ""
		if d := m.DefaultExpose(); d != nil {
			defaultService = d.Service
		}
		c := adapter.Ctx{
			Project:        m.Project.Name,
			Slug:           w.Slug,
			BaseDomain:     baseDomain,
			TraefikNetwork: network,
			Expose:         m.Expose,
			DefaultService: defaultService,
		}
		if err := adapter.AttachToTraefikNetwork(c); err == nil {
			actions = append(actions, fmt.Sprintf("reattached %s/%s on %s network", w.Project, w.Slug, network))
		}
	}
	return actions
}

func refreshDeadRoutes() []string {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return nil
	}
	cfg, err := infra.LoadConfig(paths)
	if err != nil {
		return nil
	}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		return nil
	}
	defer store.Close()

	workloads, err := store.List()
	if err != nil {
		return nil
	}
	var actions []string
	for _, w := range workloads {
		if w.ContainerID == "" || !containerAlive(w.ContainerID) {
			continue
		}
		if workloadRouteFailure(w, cfg.TLD) == "" {
			continue
		}
		m, err := manifest.Load(w.WorktreePath)
		if err != nil {
			continue
		}
		baseDomain := m.Project.BaseDomain
		if baseDomain == "" {
			baseDomain = m.Project.Name + "." + cfg.TLD
		} else {
			expanded, err := adapter.ExpandPierTokens(baseDomain, cfg.TLD)
			if err != nil {
				continue
			}
			baseDomain = expanded
		}
		defaultService := ""
		if d := m.DefaultExpose(); d != nil {
			defaultService = d.Service
		}
		c := adapter.Ctx{
			Project:        m.Project.Name,
			Slug:           w.Slug,
			BaseDomain:     baseDomain,
			TraefikNetwork: cfg.EffectiveTraefikNetwork(),
			Expose:         m.Expose,
			DefaultService: defaultService,
		}
		if err := adapter.RefreshTraefikRoutes(c); err == nil {
			actions = append(actions, fmt.Sprintf("refreshed traefik routes for %s/%s", w.Project, w.Slug))
		}
	}
	return actions
}

func workloadRouteFailure(w *state.Workload, tld string) string {
	url := workloadURL(w, tld)
	if url == "" {
		return ""
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Sprintf("route %s did not answer through traefik: %v", url, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Sprintf("route %s returned %s through traefik", url, resp.Status)
	default:
		return ""
	}
}

// pruneDeadWorkloads removes state rows whose backing container is gone.
// Returns the list of actions taken so doctor can surface them.
func pruneDeadWorkloads() []string {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return nil
	}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		return nil
	}
	defer store.Close()
	workloads, err := store.List()
	if err != nil {
		return nil
	}
	var actions []string
	for _, w := range workloads {
		if w.ContainerID == "" || containerAlive(w.ContainerID) {
			continue
		}
		if err := store.Delete(w.Project, w.Slug); err == nil {
			actions = append(actions, fmt.Sprintf("dropped orphan workload %s/%s", w.Project, w.Slug))
		}
	}
	return actions
}

func containerAlive(id string) bool {
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Status}}", id).Output()
	if err != nil {
		return false
	}
	st := strings.TrimSpace(string(out))
	return st != "" && st != "removing"
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
