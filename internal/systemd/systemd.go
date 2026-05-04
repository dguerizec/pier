// Package systemd writes/removes the pier.service unit and queries its
// status. Linux-only — other platforms get stubs in systemd_other.go.
//
// For --user it drives `systemctl --user` directly. For --system it
// shells out to `sudo` so the user gets a single-prompt UX (matches
// the behaviour of `pier install`, which already sudoes the
// systemd-resolved drop-in). Pass PrintOnly to skip exec and print
// the commands instead — useful for CI, scripted bootstraps, and
// users who route privilege escalation through their own tooling.
package systemd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// Scope picks between systemd --user (per-login) and --system (host-wide).
type Scope int

const (
	ScopeUser Scope = iota
	ScopeSystem
)

func (s Scope) String() string {
	if s == ScopeSystem {
		return "system"
	}
	return "user"
}

// ParseScope maps the --user/--system CLI flags to a Scope. Empty string
// means "auto-detect": root → system, anyone else → user.
func ParseScope(s string) (Scope, error) {
	switch s {
	case "user":
		return ScopeUser, nil
	case "system":
		return ScopeSystem, nil
	case "":
		if os.Geteuid() == 0 {
			return ScopeSystem, nil
		}
		return ScopeUser, nil
	}
	return 0, fmt.Errorf("systemd: unknown scope %q (want user|system)", s)
}

// UnitName is the systemd unit name without extension. Kept stable so
// install/uninstall/status all agree.
const UnitName = "pier"

// unitFilename is what we drop on disk.
const unitFilename = UnitName + ".service"

// Status snapshots one unit's state. Loaded=false means the unit file
// is missing (nothing installed); doctor uses that to skip the section.
type Status struct {
	Scope   Scope
	Loaded  bool
	Active  bool
	Enabled bool
	Detail  string // raw `is-active` output for the human (e.g. "activating", "failed")
}

// Render builds the unit file body. Caller passes the absolute path to
// the pier binary because os.Executable() resolves symlinks differently
// across hosts; the install command is the right place to capture it.
func Render(scope Scope, binary string) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString("Description=pier — local dev domain orchestrator\n")
	sb.WriteString("Documentation=https://github.com/LeoPartt/pier\n")
	if scope == ScopeSystem {
		sb.WriteString("After=docker.service network-online.target\n")
		sb.WriteString("Wants=docker.service network-online.target\n")
	} else {
		// User units can't depend on system units directly — systemd
		// rejects the cross-scope reference. docker.service is system,
		// so the user unit just races; the daemon retries against
		// docker until it's up.
		sb.WriteString("After=default.target\n")
	}
	sb.WriteString("\n[Service]\n")
	sb.WriteString("Type=simple\n")
	fmt.Fprintf(&sb, "ExecStart=%s serve\n", binary)
	// SIGUSR2 is the binary-swap signal (see internal/cli/serve_upgrade);
	// systemd must not interpret it as anything else. KillSignal stays
	// default SIGTERM so `systemctl stop` still works.
	sb.WriteString("Restart=on-failure\n")
	sb.WriteString("RestartSec=5\n")
	sb.WriteString("\n[Install]\n")
	if scope == ScopeSystem {
		sb.WriteString("WantedBy=multi-user.target\n")
	} else {
		sb.WriteString("WantedBy=default.target\n")
	}
	return sb.String()
}

// UnitPath returns where pier writes the unit for the given scope.
func UnitPath(scope Scope) (string, error) {
	if scope == ScopeSystem {
		return filepath.Join("/etc/systemd/system", unitFilename), nil
	}
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(u.HomeDir, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitFilename), nil
}

// InstallResult records what an Install call did. SudoSteps is
// populated only when PrintOnly was true (so callers can re-emit them
// in tests / wizards), or when the system path was bypassed because
// no tty was available for sudo.
type InstallResult struct {
	Path      string
	SudoSteps []string
}

// Install writes the unit file and activates it. For ScopeUser it
// drives `systemctl --user daemon-reload && enable --now` directly.
// For ScopeSystem it stages the unit in /tmp and shells out to sudo
// for the install/reload/enable steps; sudo prompts the user for a
// password on a tty. Set printOnly to skip the exec and print the
// commands instead.
func Install(scope Scope, binary string, printOnly bool, out io.Writer) (*InstallResult, error) {
	body := Render(scope, binary)

	if scope == ScopeSystem {
		stage, err := os.CreateTemp("", "pier-*.service")
		if err != nil {
			return nil, err
		}
		if _, err := stage.WriteString(body); err != nil {
			stage.Close()
			os.Remove(stage.Name())
			return nil, err
		}
		stage.Close()

		final, _ := UnitPath(ScopeSystem)
		fmt.Fprintf(out, "✓ staged unit: %s\n", stage.Name())
		steps := [][]string{
			{"install", "-m", "0644", stage.Name(), final},
			{"systemctl", "daemon-reload"},
			{"systemctl", "enable", "--now", UnitName},
		}

		if printOnly {
			printSudoSteps(out, "installation requires root — run:", steps)
			fmt.Fprintf(out, "  then tail logs:\n    sudo journalctl -u %s -f\n", UnitName)
			return &InstallResult{Path: final, SudoSteps: flattenSudoSteps(steps)}, nil
		}

		fmt.Fprintf(out, "  installation requires root; sudo will prompt for password\n")
		for _, step := range steps {
			if err := run(out, "sudo", step...); err != nil {
				return nil, fmt.Errorf("sudo %s: %w (rerun with --print-only to drive the steps yourself)", step[0], err)
			}
		}
		fmt.Fprintf(out, "✓ enabled and started pier.service (--system)\n")
		fmt.Fprintf(out, "  tail logs:\n    sudo journalctl -u %s -f\n", UnitName)
		return &InstallResult{Path: final}, nil
	}

	path, err := UnitPath(ScopeUser)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "✓ wrote unit: %s\n", path)

	if printOnly {
		fmt.Fprintf(out, "  --print-only: run yourself to activate:\n")
		fmt.Fprintf(out, "    systemctl --user daemon-reload\n")
		fmt.Fprintf(out, "    systemctl --user enable --now %s\n", UnitName)
		return &InstallResult{Path: path}, nil
	}

	if err := run(out, "systemctl", "--user", "daemon-reload"); err != nil {
		return nil, fmt.Errorf("daemon-reload: %w", err)
	}
	if err := run(out, "systemctl", "--user", "enable", "--now", UnitName); err != nil {
		return nil, fmt.Errorf("enable --now: %w", err)
	}
	fmt.Fprintf(out, "✓ enabled and started pier.service (--user)\n")
	fmt.Fprintf(out, "  tail logs:\n    journalctl --user -u %s -f\n", UnitName)
	return &InstallResult{Path: path}, nil
}

// Uninstall reverses Install. Missing files are not an error — the
// command is idempotent so doctor / re-runs don't fail spuriously.
func Uninstall(scope Scope, printOnly bool, out io.Writer) error {
	path, err := UnitPath(scope)
	if err != nil {
		return err
	}

	if scope == ScopeSystem {
		_, statErr := os.Stat(path)
		if statErr != nil && errors.Is(statErr, os.ErrNotExist) {
			fmt.Fprintf(out, "✓ no system unit at %s — nothing to do\n", path)
			return nil
		}
		steps := [][]string{
			{"systemctl", "disable", "--now", UnitName},
			{"rm", "-f", path},
			{"systemctl", "daemon-reload"},
		}
		if printOnly {
			printSudoSteps(out, "uninstall requires root — run:", steps)
			return nil
		}
		fmt.Fprintf(out, "  uninstall requires root; sudo will prompt for password\n")
		for _, step := range steps {
			// disable --now exits non-zero when the unit is already
			// inactive/disabled. Treat the systemctl steps as
			// best-effort so the rm + reload still happen.
			_ = run(out, "sudo", step...)
		}
		fmt.Fprintf(out, "✓ removed %s\n", path)
		return nil
	}

	if printOnly {
		fmt.Fprintf(out, "  --print-only: run yourself to remove:\n")
		fmt.Fprintf(out, "    systemctl --user disable --now %s\n", UnitName)
		fmt.Fprintf(out, "    rm -f %s\n", path)
		fmt.Fprintf(out, "    systemctl --user daemon-reload\n")
		return nil
	}
	// User scope: do the work directly; ignore "no such unit" errors.
	_ = run(out, "systemctl", "--user", "disable", "--now", UnitName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := run(out, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ removed %s\n", path)
	return nil
}

// printSudoSteps formats a list of sudo-prefixed commands for the
// --print-only path. We render the prompt prose ourselves so the
// install/uninstall blocks share the same heading style.
func printSudoSteps(out io.Writer, header string, steps [][]string) {
	fmt.Fprintf(out, "  %s\n", header)
	for _, step := range steps {
		fmt.Fprintf(out, "    sudo %s\n", strings.Join(step, " "))
	}
}

// flattenSudoSteps reformats the structured step list into a slice of
// human-readable strings, for InstallResult.SudoSteps consumers.
func flattenSudoSteps(steps [][]string) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		out = append(out, "sudo "+strings.Join(step, " "))
	}
	return out
}

// Query reports whether the unit is present and active for the given
// scope. Loaded=false short-circuits Active/Enabled — they're irrelevant
// when the unit doesn't exist.
func Query(scope Scope) Status {
	st := Status{Scope: scope}
	path, err := UnitPath(scope)
	if err != nil {
		return st
	}
	if _, err := os.Stat(path); err != nil {
		return st
	}
	st.Loaded = true

	args := []string{"is-active", UnitName}
	if scope == ScopeUser {
		args = append([]string{"--user"}, args...)
	}
	out, _ := exec.Command("systemctl", args...).Output()
	st.Detail = strings.TrimSpace(string(out))
	st.Active = st.Detail == "active"

	args = []string{"is-enabled", UnitName}
	if scope == ScopeUser {
		args = append([]string{"--user"}, args...)
	}
	out, _ = exec.Command("systemctl", args...).Output()
	enabled := strings.TrimSpace(string(out))
	st.Enabled = enabled == "enabled" || enabled == "static" || enabled == "alias"
	return st
}

func run(out io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
