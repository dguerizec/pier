//go:build linux

package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const resolvedDropinPath = "/etc/systemd/resolved.conf.d/pier.conf"

// A working per-link Tailscale route makes the global drop-in redundant.
// Keeping it exposes a zone-only server to clients of resolved's flattened
// resolv.conf (including Docker), which cannot preserve split-DNS routing.
func configureHostDNS(tld, dnsIP, answerIP string) (bool, error) {
	return configureHostDNSAt(tld, dnsIP, resolvedDropinPath,
		detectHostDNSBackendFor(tld, answerIP), runSudo)
}

func configureHostDNSAt(tld, dnsIP, path string, backend hostDNSBackend, sudo func(...string) error) (bool, error) {
	switch backend {
	case hostDNSVerifiedTailscale:
		body, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !bytes.HasPrefix(body, []byte("# managed by pier\n")) {
			return false, fmt.Errorf("refusing to remove unmanaged DNS drop-in %s", path)
		}
		if err := sudo("rm", "-f", path); err != nil {
			return false, err
		}
		if err := sudo("systemctl", "reload-or-restart", "systemd-resolved"); err != nil {
			return false, err
		}
		return true, nil
	case hostDNSTailscale:
		return false, nil
	case hostDNSManual:
		return false, ErrManualDNSNeeded
	}
	body := renderResolvedDropin(tld, dnsIP)

	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, body) {
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

	if err := sudo("install", "-m", "0644", "-D", tmpPath, path); err != nil {
		return false, fmt.Errorf("install drop-in (you may need to enter your password): %w", err)
	}
	if err := sudo("systemctl", "reload-or-restart", "systemd-resolved"); err != nil {
		return false, fmt.Errorf("reload systemd-resolved: %w", err)
	}
	return true, nil
}

func unconfigureHostDNS() (bool, error) {
	if _, err := os.Stat(resolvedDropinPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err := runSudo("rm", "-f", resolvedDropinPath); err != nil {
		return false, err
	}
	if err := runSudo("systemctl", "reload-or-restart", "systemd-resolved"); err != nil {
		return false, err
	}
	return true, nil
}

// manualDNSInstructions returns the shell commands the user should run when
// pier cannot or should not modify host DNS itself. Two flavours: when
// systemd-resolved is the active resolver pier emits a ready-to-paste
// drop-in; otherwise we fall back to a generic guidance that names the
// target IP and lets the user adapt to whichever resolver they run.
func manualDNSInstructions(tld, dnsIP string) string {
	if systemdResolvedActive() {
		body := renderResolvedDropin(tld, dnsIP)
		return fmt.Sprintf(`Run the following as root to route .%s lookups to dnsmasq:

  sudo tee %s >/dev/null <<'EOF'
%s
EOF
  sudo systemctl reload-or-restart systemd-resolved

Verify with:  dig +short @127.0.0.1 anything.%s
`, tld, resolvedDropinPath, string(body), tld)
	}
	return fmt.Sprintf(`pier auto-configures host DNS only on systemd-resolved (Linux) and via tailscale's MagicDNS — neither was detected here.

Configure your resolver so .%s queries forward to %s:53. Examples:
  - NetworkManager + dnsmasq plugin: drop a server=/.%s/%s file under /etc/NetworkManager/dnsmasq.d/
  - resolvconf: append nameserver %s to a hook for the .%s domain
  - /etc/resolv.conf direct: add `+"`nameserver %s`"+` (affects all queries — narrow it via your stack instead)

Verify with:  dig +short @127.0.0.1 anything.%s
`, tld, dnsIP, tld, dnsIP, dnsIP, tld, dnsIP, tld)
}

// systemdResolvedActive checks whether systemd-resolved is running.
func systemdResolvedActive() bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", "systemd-resolved")
	return cmd.Run() == nil
}

type hostDNSBackend int

const (
	hostDNSManual hostDNSBackend = iota
	hostDNSTailscale
	hostDNSSystemdResolved
	hostDNSVerifiedTailscale
)

func selectHostDNSBackend(tailscaleResolvConf, resolvedActive, tailscaleDNSActive bool) hostDNSBackend {
	switch {
	case tailscaleResolvConf:
		return hostDNSTailscale
	case resolvedActive:
		return hostDNSSystemdResolved
	case tailscaleDNSActive:
		return hostDNSTailscale
	default:
		return hostDNSManual
	}
}

func detectHostDNSBackend() hostDNSBackend {
	return selectHostDNSBackend(
		tailscaleOwnsResolvConf(),
		systemdResolvedActive(),
		tailscaleDNSActive(),
	)
}

func detectHostDNSBackendFor(tld, answerIP string) hostDNSBackend {
	backend := detectHostDNSBackend()
	if backend == hostDNSSystemdResolved && verifiedTailscaleRoute(tld, answerIP, dnsCommand) {
		return hostDNSVerifiedTailscale
	}
	return backend
}

func dnsCommand(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "resolvectl", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Output()
}

// Constrain the query to Tailscale so the existing global Pier drop-in cannot
// provide false evidence. Require an exact route and the expected installation's
// address: another peer may own the same TLD.
func verifiedTailscaleRoute(tld, answerIP string, run func(...string) ([]byte, error)) bool {
	expected := net.ParseIP(answerIP)
	if expected == nil {
		return false
	}
	domains, err := run("domain", "tailscale0")
	if err != nil {
		return false
	}
	found := false
	for _, domain := range strings.Fields(string(domains)) {
		if strings.TrimPrefix(domain, "~") == tld {
			found = true
		}
	}
	if !found {
		return false
	}
	hostname := fmt.Sprintf("pier-dns-probe-%d.%s", time.Now().UnixNano(), tld)
	out, err := run("--json=short", "--cache=no", "--type=A", "--interface=tailscale0", "query", hostname)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	matched := false
	for {
		var record struct {
			Key struct {
				Class int
				Type  int
				Name  string
			}
			Address []int
		}
		err := decoder.Decode(&record)
		if err == io.EOF {
			return matched
		}
		if err != nil {
			return false
		}
		if record.Key.Class != 1 || record.Key.Type != 1 || record.Key.Name != hostname || len(record.Address) != 4 {
			return false
		}
		ip := make(net.IP, 4)
		for i, octet := range record.Address {
			if octet < 0 || octet > 255 {
				return false
			}
			ip[i] = byte(octet)
		}
		if !ip.Equal(expected) {
			return false
		}
		matched = true
	}
}

func tailscaleOwnsResolvConf() bool {
	if body, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		if line, _, _ := strings.Cut(string(body), "\n"); strings.Contains(line, "generated by tailscale") {
			return true
		}
	}
	return false
}

func tailscaleDNSActive() bool {
	out, err := exec.Command("tailscale", "dns", "status").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Tailscale DNS: enabled")
}

func checkResolvedDropin(tld, answerIP string) Check {
	switch detectHostDNSBackendFor(tld, answerIP) {
	case hostDNSVerifiedTailscale:
		if _, err := os.Stat(resolvedDropinPath); !errors.Is(err, os.ErrNotExist) {
			return Check{Name: "host DNS routing", Status: StatusWarn,
				Detail:  "working Tailscale route; redundant global Pier DNS drop-in can break Docker DNS",
				FixHint: "pier doctor --fix"}
		}
		return Check{Name: "host DNS routing", Status: StatusPass,
			Detail: "verified Tailscale split-DNS route for ." + tld}
	case hostDNSTailscale:
		return Check{
			Name:   "host DNS routing",
			Status: StatusPass,
			Detail: "tailscale split-DNS forwards ." + tld + " queries (no drop-in needed)",
		}
	case hostDNSManual:
		return Check{
			Name:    "host DNS routing",
			Status:  StatusFail,
			Detail:  "neither systemd-resolved nor Tailscale DNS manages host lookups",
			FixHint: "configure host DNS manually",
		}
	}
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

// needsResolvedRewrite also detects a redundant drop-in even when its
// contents still match the active installation.
func needsResolvedRewrite(tld, bindIP, answerIP string) bool {
	if detectHostDNSBackendFor(tld, answerIP) == hostDNSVerifiedTailscale {
		_, err := os.Stat(resolvedDropinPath)
		return !errors.Is(err, os.ErrNotExist)
	}
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
