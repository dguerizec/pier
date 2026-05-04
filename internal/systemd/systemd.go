// Package systemd writes/removes the pier.service user unit and
// queries its status. Linux-only — other platforms get stubs in
// systemd_other.go.
//
// User scope only: pier targets a single human's session, with
// docker access already configured in their account. A system-wide
// unit was prototyped but dropped — multi-tenant servers and
// dedicated daemon accounts aren't pier's user model. systemctl
// --user is driven directly. PrintOnly skips exec and emits the
// commands instead, for CI / scripted bootstraps.
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

// UnitName is the systemd unit name without extension. Kept stable so
// install/uninstall/status all agree.
const UnitName = "pier"

// unitFilename is what we drop on disk.
const unitFilename = UnitName + ".service"

// Status snapshots the unit's state. Loaded=false means the unit file
// is missing (nothing installed); doctor uses that to skip the section.
type Status struct {
	Loaded  bool
	Active  bool
	Enabled bool
	Detail  string // raw `is-active` output for the human (e.g. "activating", "failed")
}

// Render builds the unit file body. Caller passes the absolute path to
// the pier binary because os.Executable() resolves symlinks differently
// across hosts; the install command is the right place to capture it.
func Render(binary string) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString("Description=pier — local dev domain orchestrator\n")
	sb.WriteString("Documentation=https://github.com/LeoPartt/pier\n")
	// User units can't depend on system units (cross-scope After= is
	// rejected by systemd). docker.service is system-scope, so we
	// just race; the daemon retries against docker until it's up.
	sb.WriteString("After=default.target\n")
	sb.WriteString("\n[Service]\n")
	sb.WriteString("Type=simple\n")
	fmt.Fprintf(&sb, "ExecStart=%s serve\n", binary)
	// SIGUSR2 is the binary-swap signal (see internal/cli/serve_upgrade);
	// systemd must not interpret it as anything else. KillSignal stays
	// default SIGTERM so `systemctl stop` still works.
	sb.WriteString("Restart=on-failure\n")
	sb.WriteString("RestartSec=5\n")
	sb.WriteString("\n[Install]\n")
	sb.WriteString("WantedBy=default.target\n")
	return sb.String()
}

// UnitPath returns where pier writes the user unit. Honours
// $XDG_CONFIG_HOME like the rest of the user's systemd setup.
func UnitPath() (string, error) {
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

// InstallResult records what an Install call did. Path is the unit
// file location (or where it would land in --print-only).
type InstallResult struct {
	Path string
}

// Install writes the user unit and (unless printOnly) drives
// `systemctl --user daemon-reload && enable --now`. PrintOnly emits
// the commands so the caller can run them via their own tooling.
func Install(binary string, printOnly bool, out io.Writer) (*InstallResult, error) {
	body := Render(binary)
	path, err := UnitPath()
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
	fmt.Fprintf(out, "✓ enabled and started pier.service\n")
	fmt.Fprintf(out, "  tail logs:\n    journalctl --user -u %s -f\n", UnitName)
	return &InstallResult{Path: path}, nil
}

// Uninstall removes the user unit. Missing files aren't an error —
// the command is idempotent so doctor / re-runs don't fail spuriously.
func Uninstall(printOnly bool, out io.Writer) error {
	path, err := UnitPath()
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(path); statErr != nil && errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintf(out, "✓ no unit at %s — nothing to do\n", path)
		return nil
	}
	if printOnly {
		fmt.Fprintf(out, "  --print-only: run yourself to remove:\n")
		fmt.Fprintf(out, "    systemctl --user disable --now %s\n", UnitName)
		fmt.Fprintf(out, "    rm -f %s\n", path)
		fmt.Fprintf(out, "    systemctl --user daemon-reload\n")
		return nil
	}
	// disable --now exits non-zero when the unit is already stopped; not fatal.
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

// Query reports whether the user unit is present and active.
// Loaded=false short-circuits Active/Enabled — they're irrelevant
// when the unit file doesn't exist.
func Query() Status {
	st := Status{}
	path, err := UnitPath()
	if err != nil {
		return st
	}
	if _, err := os.Stat(path); err != nil {
		return st
	}
	st.Loaded = true

	out, _ := exec.Command("systemctl", "--user", "is-active", UnitName).Output()
	st.Detail = strings.TrimSpace(string(out))
	st.Active = st.Detail == "active"

	out, _ = exec.Command("systemctl", "--user", "is-enabled", UnitName).Output()
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
