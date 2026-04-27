package cli

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/state"
)

type lsRow struct {
	Project string `json:"project"`
	Slug    string `json:"slug"`
	URL     string `json:"url"`
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
}

func newLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List active workloads across all projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := infra.DefaultPaths()
			if err != nil {
				return err
			}
			if _, err := infra.LoadConfig(paths); err != nil {
				return err
			}
			store, err := state.Open(paths.StateDB)
			if err != nil {
				return err
			}
			defer store.Close()

			workloads, err := store.List()
			if err != nil {
				return err
			}

			rows := make([]lsRow, 0, len(workloads))
			for _, w := range workloads {
				baseDomain, _ := baseDomainFor(w.WorktreePath)
				if baseDomain == "" {
					// Manifest gone — fall back to project.tld so the user
					// still gets something usable.
					baseDomain = w.Project + ".test"
				}
				rows = append(rows, lsRow{
					Project: w.Project,
					Slug:    w.Slug,
					URL:     adapter.URL(w.Slug, baseDomain),
					Status:  containerStatus(w),
					Uptime:  humanUptime(time.Since(w.StartedAt)),
				})
			}

			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			return renderTable(cmd, rows)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON output")
	return cmd
}

func renderTable(cmd *cobra.Command, rows []lsRow) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSLUG\tURL\tSTATUS\tUPTIME")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Project, r.Slug, r.URL, r.Status, r.Uptime)
	}
	return w.Flush()
}

// baseDomainFor reads <worktree>/.pier.toml and returns project.base_domain.
// Skips validation so stale or partially-broken manifests still render
// something usable in `pier ls`.
func baseDomainFor(worktreePath string) (string, error) {
	var stub struct {
		Project struct {
			BaseDomain string `toml:"base_domain"`
		} `toml:"project"`
	}
	if _, err := toml.DecodeFile(filepath.Join(worktreePath, manifest.FileName), &stub); err != nil {
		return "", err
	}
	return stub.Project.BaseDomain, nil
}

// containerStatus reports the runtime state of a workload by inspecting docker.
func containerStatus(w *state.Workload) string {
	if w.ContainerID == "" {
		return "unknown"
	}
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Status}}", w.ContainerID).Output()
	if err != nil {
		return "missing"
	}
	return strings.TrimSpace(string(out))
}

func humanUptime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
