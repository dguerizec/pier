//go:build linux

package infra

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const resolvedDropinPath = "/etc/systemd/resolved.conf.d/pier.conf"

// configureHostDNS writes the systemd-resolved drop-in so .<tld> queries
// hit our dnsmasq. Requires sudo (interactively prompts the user) only
// when the drop-in is missing or its content needs to change.
//
// Returns ErrManualDNSNeeded if systemd-resolved is not the active resolver
// — caller should fall back to the manual instructions path.
//
// The bool return reports whether anything was actually written; callers
// use it to decide what to print and whether to mention sudo at all.
func configureHostDNS(tld, dnsIP string) (changed bool, err error) {
	if !systemdResolvedActive() {
		return false, ErrManualDNSNeeded
	}
	body := renderResolvedDropin(tld, dnsIP)

	if existing, err := os.ReadFile(resolvedDropinPath); err == nil && bytes.Equal(existing, body) {
		return false, nil
	}

	tmp, err := os.CreateTemp("", "pier-resolved-*.conf")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return false, err
	}
	tmp.Close()

	if err := runSudo("install", "-m", "0644", "-D", tmpPath, resolvedDropinPath); err != nil {
		return false, fmt.Errorf("install drop-in (you may need to enter your password): %w", err)
	}
	if err := runSudo("systemctl", "reload-or-restart", "systemd-resolved"); err != nil {
		return false, fmt.Errorf("reload systemd-resolved: %w", err)
	}
	return true, nil
}

func unconfigureHostDNS() error {
	if _, err := os.Stat(resolvedDropinPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := runSudo("rm", "-f", resolvedDropinPath); err != nil {
		return err
	}
	return runSudo("systemctl", "reload-or-restart", "systemd-resolved")
}

// manualDNSInstructions returns the shell commands the user should run when
// pier cannot or should not modify host DNS itself.
func manualDNSInstructions(tld, dnsIP string) string {
	body := renderResolvedDropin(tld, dnsIP)
	return fmt.Sprintf(`Run the following as root to route .%s lookups to dnsmasq:

  sudo tee %s >/dev/null <<'EOF'
%s
EOF
  sudo systemctl reload-or-restart systemd-resolved

Verify with:  dig +short @127.0.0.1 anything.%s
`, tld, resolvedDropinPath, string(body), tld)
}

// systemdResolvedActive checks whether systemd-resolved is running.
func systemdResolvedActive() bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", "systemd-resolved")
	return cmd.Run() == nil
}

func checkResolvedDropin(tld string) Check {
	body, err := os.ReadFile(resolvedDropinPath)
	if errors.Is(err, os.ErrNotExist) {
		return Check{
			Name:    "systemd-resolved drop-in",
			Status:  StatusFail,
			Detail:  resolvedDropinPath + " missing",
			FixHint: "pier doctor --fix  (re-runs the sudo install step)",
		}
	}
	if err != nil {
		return Check{Name: "systemd-resolved drop-in", Status: StatusWarn, Detail: err.Error()}
	}
	if !strings.Contains(string(body), "Domains=~"+tld) {
		return Check{
			Name:    "systemd-resolved drop-in",
			Status:  StatusFail,
			Detail:  "Domains=~" + tld + " not found",
			FixHint: "pier doctor --fix",
		}
	}
	return Check{Name: "systemd-resolved drop-in", Status: StatusPass}
}

// needsResolvedRewrite returns true when the on-disk drop-in is missing or
// references a different (TLD, bindIP) than the active config.
func needsResolvedRewrite(tld, bindIP string) bool {
	body, err := os.ReadFile(resolvedDropinPath)
	if err != nil {
		return true
	}
	want := string(renderResolvedDropin(tld, bindIP))
	return string(body) != want
}

func runSudo(args ...string) error {
	cmd := exec.Command("sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
