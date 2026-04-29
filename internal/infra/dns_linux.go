//go:build linux

package infra

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

const resolvedDropinPath = "/etc/systemd/resolved.conf.d/pier.conf"

// configureHostDNS writes the systemd-resolved drop-in so .<tld> queries
// hit our dnsmasq. Requires sudo (interactively prompts the user).
//
// Returns ErrManualDNSNeeded if systemd-resolved is not the active resolver
// — caller should fall back to the manual instructions path.
func configureHostDNS(tld, dnsIP string) error {
	if !systemdResolvedActive() {
		return ErrManualDNSNeeded
	}
	body := renderResolvedDropin(tld, dnsIP)

	tmp, err := os.CreateTemp("", "pier-resolved-*.conf")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	if err := runSudo("install", "-m", "0644", "-D", tmpPath, resolvedDropinPath); err != nil {
		return fmt.Errorf("install drop-in (you may need to enter your password): %w", err)
	}
	if err := runSudo("systemctl", "reload-or-restart", "systemd-resolved"); err != nil {
		return fmt.Errorf("reload systemd-resolved: %w", err)
	}
	return nil
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

func runSudo(args ...string) error {
	cmd := exec.Command("sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
