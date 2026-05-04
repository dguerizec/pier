package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/state"
	"github.com/LeoPartt/pier/internal/systemd"
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
			appendServeUnitChecks(&fixed)
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

// appendStateChecks adds one check per workload row, marking rows whose
// container is gone as failures.
func appendStateChecks(r *infra.Report) {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return
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
		r.Checks = append(r.Checks, infra.Check{Name: name, Status: infra.StatusPass})
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
